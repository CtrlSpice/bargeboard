package f1staticreceiver

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver"
)

// An invented six-second qualifying lap. Geometry uses the same local coordinate
// system as the compressed frame; neither archive needs a live service.
func signalFixture(t *testing.T) FrozenReplayPlan {
	t.Helper()
	var compressed bytes.Buffer
	w, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(w, `{"Position":[{"Timestamp":"2025-03-10T10:00:03Z","Entries":{"17":{"X":20,"Y":-40,"Z":10}}}]}`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"/s/DriverList.json": drivers, "/s/SessionInfo.json": strings.ReplaceAll(info, `"Race"`, `"Qualifying"`),
		"/s/SessionData.jsonStream": `00:00:00.000{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}
00:00:09.000{"StatusSeries":{"1":{"Utc":"2025-03-10T10:00:09Z","SessionStatus":"Ends"}}}`,
		"/s/Position.z.jsonStream": fmt.Sprintf("\xef\xbb\xbf00:00:03.000%q\r\n", base64.StdEncoding.EncodeToString(compressed.Bytes())),
		"/s/TimingData.jsonStream": `00:00:01.000{"Lines":{"17":{"SessionPart":1,"NumberOfLaps":1,"Position":"2"}}}
00:00:03.000{"Lines":{"17":{"Sectors":{"0":{"Value":"2.000"}},"Position":"1"}}}
00:00:05.000{"Lines":{"17":{"Sectors":{"1":{"Value":"2.000"}}}}}
00:00:07.000{"Lines":{"17":{"NumberOfLaps":2,"Sectors":{"2":{"Value":"2.000"}},"LastLapTime":{"Value":"6.000"}}}}`,
		"/s/ExtrapolatedClock.jsonStream": `00:00:00.000{"Utc":"2025-03-10T10:00:00Z","Remaining":"00:01:00","Extrapolating":true}`,
		"/3/2025":                         `{"year":2025,"circuitKey":3,"corners":[{"number":7,"trackPosition":{"x":20,"y":-40}}],"marshalSectors":[{"number":3,"trackPosition":{"x":20,"y":-40}}]}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/3/2025" {
			if r.Header.Get("Authorization") != "" {
				t.Error("archive credential reached circuit service")
			}
		} else if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing archive token")
		}
		if data, ok := files[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, data)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.SessionPath = "s"
	cfg.Endpoint = server.URL
	cfg.Auth.TokenFile = filepath.Join(t.TempDir(), "archive-token")
	if err := os.WriteFile(cfg.Auth.TokenFile, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.CircuitEnrichment.Enabled = true
	cfg.CircuitEnrichment.Endpoint = server.URL
	p, err := Materialize(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestSignalReplay(t *testing.T) {
	p := signalFixture(t)
	host := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var want [3][]string
	for _, pace := range []string{"instant", "1x", "10x", "100x"} {
		for _, fail := range []int{-1, 0, 1, 2} {
			t.Run(fmt.Sprintf("%s/failure%d", pace, fail), func(t *testing.T) {
				var got [3][]string
				var names []string
				var coord, clockPoints, lapCount, sectorCount int
				sink := func(signal int, body []byte, err error) error {
					if err != nil {
						t.Fatal(err)
					}
					got[signal] = append(got[signal], string(body))
					if signal == fail {
						return errors.New("invented consumer failure")
					}
					return nil
				}
				cfg := DefaultConfig()
				cfg.Pace = pace
				r := newReceiver(cfg, receiver.Settings{})
				clock := &fakeClock{host}
				r.clock = clock
				r.traces, _ = consumer.NewTraces(func(_ context.Context, tr ptrace.Traces) error {
					trace, _ := f1id.DriverTraceID(p.Identity, 17)
					root, _ := f1id.DriverSessionRootSpanID(p.Identity, 17)
					phase, _ := f1id.QualifyingPhaseSpanID(p.Identity, 17, f1id.PhaseQ1)
					stint, _ := f1id.StintSpanID(p.Identity, 17, f1id.PhaseQ1, 1)
					ss := tr.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
					for i := 0; i < ss.Len(); i++ {
						sp := ss.At(i)
						id, parent := root, pcommon.SpanID{}
						switch sp.Name() {
						case "qualifying.phase":
							id, parent = phase, root
						case "stint":
							id, parent = stint, phase
						case "lap", "sector":
							lap, _ := sp.Attributes().Get("f1.lap.number")
							lapID, _ := f1id.LapSpanID(p.Identity, 17, f1id.PhaseQ1, lap.Int())
							id, parent = lapID, stint
							if sp.Name() == "sector" {
								n, _ := sp.Attributes().Get("f1.sector.number")
								id, _ = f1id.SectorSpanID(p.Identity, 17, f1id.PhaseQ1, lap.Int(), n.Int())
								parent = lapID
							}
						}
						if sp.TraceID() != trace || sp.SpanID() != id || sp.ParentSpanID() != parent {
							t.Fatalf("wrong %s identity", sp.Name())
						}
						names = append(names, sp.Name())
						for j := 0; j < sp.Events().Len(); j++ {
							ev := sp.Events().At(j)
							if ev.Name() == "f1.position.changed" {
								coord++
								a := ev.Attributes().AsRaw()
								if a["f1.position.x"] != 2. || a["f1.position.y"] != -4. || a["f1.position.sample_age"] != 0. || a["f1.circuit.corner.number"] != int64(7) || a["f1.circuit.marshal_sector"] != int64(3) {
									t.Fatal(a)
								}
							}
						}
					}
					b, e := (&ptrace.JSONMarshaler{}).MarshalTraces(tr)
					return sink(0, b, e)
				})
				r.metrics, _ = consumer.NewMetrics(func(_ context.Context, m pmetric.Metrics) error {
					ms := m.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
					for i := 0; i < ms.Len(); i++ {
						x := ms.At(i)
						if x.Name() == "f1.session.clock.remaining" {
							clockPoints += x.Gauge().DataPoints().Len()
						}
						if x.Type() != pmetric.MetricTypeHistogram {
							continue
						}
						for j := 0; j < x.Histogram().DataPoints().Len(); j++ {
							d := x.Histogram().DataPoints().At(j)
							if x.Histogram().AggregationTemporality() != pmetric.AggregationTemporalityDelta || d.Count() != 1 || d.StartTimestamp().AsTime().Second() != 0 || d.Timestamp().AsTime().Second() != 9 {
								t.Fatal("wrong window", x.Name())
							}
							switch x.Name() {
							case "f1.driver.lap_time":
								lapCount++
								if d.Sum() != 6 || d.ExplicitBounds().Len() != 74 {
									t.Fatal("lap population")
								}
							case "f1.driver.sector_time":
								sectorCount++
								if d.Sum() != 2 || d.ExplicitBounds().Len() != 78 {
									t.Fatal("sector population")
								}
							}
						}
					}
					b, e := (&pmetric.JSONMarshaler{}).MarshalMetrics(m)
					return sink(1, b, e)
				})
				r.logs, _ = consumer.NewLogs(func(_ context.Context, l plog.Logs) error {
					ls := l.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
					for i := 0; i < ls.Len(); i++ {
						x := ls.At(i)
						if x.ObservedTimestamp() != pcommon.NewTimestampFromTime(clock.Now()) || x.Timestamp().AsTime().Year() != 2025 {
							t.Fatal("log clocks")
						}
						x.SetObservedTimestamp(0)
					}
					b, e := (&plog.JSONMarshaler{}).MarshalLogs(l)
					return sink(2, b, e)
				})
				err := r.play(t.Context(), p)
				if (err != nil) != (fail >= 0) {
					t.Fatal(err)
				}
				if coord != 1 || clockPoints != 1 || lapCount != 1 || sectorCount != 3 || !reflect.DeepEqual(names, []string{"sector", "sector", "sector", "lap", "lap", "stint", "qualifying.phase", "driver.session"}) {
					t.Fatal(names, coord, clockPoints, lapCount, sectorCount)
				}
				if want[0] == nil {
					want = got
				} else if !reflect.DeepEqual(want, got) {
					t.Fatal("racing signals changed")
				}
			})
		}
	}
}
func TestWindowBoundaries(t *testing.T) {
	epoch := time.Unix(0, 0).UTC()
	start := epoch.Add(3 * time.Second)
	for _, c := range []struct{ at, lo, hi time.Duration }{{3e9, 3e9, 10e9}, {10e9 - 1, 3e9, 10e9}, {10e9, 3e9, 10e9}, {10e9 + 1, 10e9, 20e9}} {
		lo, hi := window(epoch.Add(c.at), start)
		if lo.Sub(epoch) != c.lo || hi.Sub(epoch) != c.hi {
			t.Fatal(c, lo, hi)
		}
	}
}

func TestPacingDoesNotEnterReducer(t *testing.T) {
	p := signalFixture(t)
	reference := newSession(p)
	want := make([]effects, len(p.batches))
	for i, b := range p.batches {
		want[i] = reference.apply(b, i == len(p.batches)-1)
	}
	for _, pace := range []string{"instant", "1x", "10x", "100x"} {
		s := newSession(p)
		got := []effects{}
		clock := &fakeClock{time.Unix(2_000_000_000, 0)}
		err := Replay(t.Context(), p, pace, clock, func(_ context.Context, b FrozenBatch, _ time.Time) error {
			got = append(got, s.apply(b, b.Ordinal == int64(len(p.batches)-1)))
			return nil
		})
		if err != nil || !reflect.DeepEqual(reference, s) || !reflect.DeepEqual(want, got) {
			t.Fatal("reducer state or per-ordinal effects differ", pace, err)
		}
	}
}

type recordingStatusHost struct {
	events chan componentstatus.Status
}

func (*recordingStatusHost) GetExtensions() map[component.ID]component.Component { return nil }
func (h *recordingStatusHost) Report(event *componentstatus.Event) {
	h.events <- event.Status()
}

func TestReceiverLifecycleStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok/SessionInfo.json", "/mirror/SessionInfo.json":
			fmt.Fprint(w, info)
		case "/ok/DriverList.json", "/mirror/DriverList.json":
			fmt.Fprint(w, drivers)
		case "/ok/Heartbeat.jsonStream", "/mirror/Heartbeat.jsonStream":
			fmt.Fprint(w, `00:00:00.000{"Utc":"2025-03-10T10:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	disabled := newReceiver(DefaultConfig(), receiver.Settings{})
	host := &recordingStatusHost{events: make(chan componentstatus.Status, 4)}
	if err := disabled.Start(t.Context(), host); err != nil {
		t.Fatal(err)
	}
	if len(host.events) != 0 {
		t.Fatalf("disabled receiver reported status: %d", len(host.events))
	}
	if err := disabled.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path string
		want componentstatus.Status
	}{
		{"ok", componentstatus.StatusOK},
		{"missing", componentstatus.StatusPermanentError},
	} {
		t.Run(tc.path, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Enabled = true
			cfg.Endpoint = server.URL
			cfg.SessionPath = tc.path
			r := newReceiver(cfg, receiver.Settings{})
			h1 := &recordingStatusHost{events: make(chan componentstatus.Status, 4)}
			h2 := &recordingStatusHost{events: make(chan componentstatus.Status, 4)}
			if err := r.Start(t.Context(), h1); err != nil {
				t.Fatal(err)
			}
			if got := <-h1.events; got != componentstatus.StatusRecoverableError {
				t.Fatalf("initial status = %v, want RecoverableError", got)
			}
			if got := <-h1.events; got != tc.want {
				t.Fatalf("terminal status = %v, want %v", got, tc.want)
			}
			if err := r.Start(t.Context(), h2); err != nil {
				t.Fatal(err)
			}
			if got := <-h2.events; got != tc.want {
				t.Fatalf("late pipeline status = %v, want %v", got, tc.want)
			}
			if err := r.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			r.status.Report(componentstatus.NewEvent(componentstatus.StatusOK))
			h3 := &recordingStatusHost{events: make(chan componentstatus.Status, 4)}
			if err := r.Start(t.Context(), h3); !errors.Is(err, errReceiverStopped) || len(h1.events) != 0 || len(h3.events) != 0 {
				t.Fatalf("post-shutdown Start err=%v h1=%d h3=%d", err, len(h1.events), len(h3.events))
			}
		})
	}

	t.Run("duplicate_canonical_session_across_mirror_paths", func(t *testing.T) {
		factory := NewFactory()
		sink, _ := consumer.NewTraces(func(context.Context, ptrace.Traces) error { return nil })

		cfg1 := factory.CreateDefaultConfig().(*Config)
		cfg1.Enabled = true
		cfg1.Endpoint = server.URL
		cfg1.SessionPath = "ok"
		r1, err := factory.CreateTraces(t.Context(), receiver.Settings{ID: component.MustNewIDWithName("f1static", "a")}, cfg1, sink)
		if err != nil {
			t.Fatal(err)
		}
		cfg2 := factory.CreateDefaultConfig().(*Config)
		cfg2.Enabled = true
		cfg2.Endpoint = server.URL
		cfg2.SessionPath = "mirror"
		r2, err := factory.CreateTraces(t.Context(), receiver.Settings{ID: component.MustNewIDWithName("f1static", "b")}, cfg2, sink)
		if err != nil {
			t.Fatal(err)
		}

		h1 := &recordingStatusHost{events: make(chan componentstatus.Status, 4)}
		if err := r1.Start(t.Context(), h1); err != nil {
			t.Fatal(err)
		}
		if <-h1.events != componentstatus.StatusRecoverableError || <-h1.events != componentstatus.StatusOK {
			t.Fatal("first archive did not complete cleanly")
		}

		h2 := &recordingStatusHost{events: make(chan componentstatus.Status, 4)}
		if err := r2.Start(t.Context(), h2); err != nil {
			t.Fatal(err)
		}
		if <-h2.events != componentstatus.StatusRecoverableError || <-h2.events != componentstatus.StatusPermanentError {
			t.Fatal("mirror archive serving identical SessionIdentity was not rejected")
		}

		if err := r1.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := r2.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}

		// Shutdown releases the identity reservation for subsequent runs.
		r3, err := factory.CreateTraces(t.Context(), receiver.Settings{ID: component.MustNewIDWithName("f1static", "b")}, cfg2, sink)
		if err != nil {
			t.Fatal(err)
		}
		h3 := &recordingStatusHost{events: make(chan componentstatus.Status, 4)}
		if err := r3.Start(t.Context(), h3); err != nil {
			t.Fatal(err)
		}
		if <-h3.events != componentstatus.StatusRecoverableError || <-h3.events != componentstatus.StatusOK {
			t.Fatal("released identity could not replay after Shutdown")
		}
		if err := r3.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}
