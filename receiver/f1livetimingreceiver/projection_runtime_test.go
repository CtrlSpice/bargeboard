package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestProtocolFailureFlushesAcceptedDataOnShutdown(t *testing.T) {
	first := strings.SplitN(projectionSession(t), "\x1e", 3)
	wire := strings.Join(first[:2], "\x1e") + "\x1e" + `{"type":1,"target":"feed","arguments":["PitStop",{"RacingNumber":"1","Lap":"2","PitStopTime":"2.3"},"1970-01-01T00:00:04Z"]}` + "\x1e{\x1e"
	server := newConnectionTestServer(t, func(socket *websocket.Conn) {
		_, _, _ = socket.Read(t.Context())
		_ = socket.Write(t.Context(), websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = socket.Read(t.Context())
		_ = socket.Write(t.Context(), websocket.MessageText, []byte(wire))
	})
	r := newLiveTimingReceiver(connectionTestConfig(t, server.URL), receivertest.NewNopSettings(Type))
	ts, ms := &consumertest.TracesSink{}, &consumertest.MetricsSink{}
	r.registerTraces(ts)
	r.registerMetrics(ms)
	if err := r.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := waitForCompletion(ctx, r.done); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if ts.SpanCount() != 1 || ms.DataPointCount() != 1 {
			t.Fatalf("shutdown exported %d spans, %d points", ts.SpanCount(), ms.DataPointCount())
		}
	}
	m := ms.AllMetrics()[0].ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().At(0)
	p := m.Histogram().DataPoints().At(0)
	if m.Name() != "f1.pit.stop_duration" || p.Sum() != 2.3 || p.StartTimestamp() != 1e9 || p.Timestamp() != 4e9 {
		t.Fatal(m.Name(), p.Sum(), p.StartTimestamp(), p.Timestamp())
	}

	// A blocked failing consumer during terminal flush must honor Shutdown deadlines
	// and record its failure before the final input summary.
	rBlocked := newLiveTimingReceiver(connectionTestConfig(t, server.URL), receivertest.NewNopSettings(Type))
	entered, release := make(chan struct{}), make(chan struct{})
	blockedTraces, err := consumer.NewTraces(func(delivery context.Context, _ ptrace.Traces) error {
		if delivery.Err() != nil {
			t.Errorf("terminal delivery context canceled: %v", delivery.Err())
		}
		close(entered)
		<-release
		return errors.New("synthetic terminal flush failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	rBlocked.registerTraces(blockedTraces)
	if err := rBlocked.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("terminal flush did not reach consumer")
	}
	shortCtx, shortCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer shortCancel()
	if err := rBlocked.Shutdown(shortCtx); !errors.Is(err, context.DeadlineExceeded) || rBlocked.operational.state.Load().summarized {
		t.Fatalf("blocked terminal flush = %v, state = %+v", err, rBlocked.operational.state.Load())
	}
	close(release)
	if err := rBlocked.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if s := rBlocked.operational.state.Load(); !s.summarized || s.consumerFailures != 1 {
		t.Fatalf("terminal summary state = %+v", s)
	}
}

func TestTransportLossCancelsTimerDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	blocked, reconnected := make(chan struct{}), make(chan struct{})
	var connections atomic.Int32
	server := newConnectionTestServer(t, func(c *websocket.Conn) {
		n := connections.Add(1)
		_, _, _ = c.Read(ctx)
		_ = c.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = c.Read(ctx)
		if n == 1 {
			select {
			case <-blocked:
			case <-ctx.Done():
			}
			return
		}
		close(reconnected)
		_, _, _ = c.Read(ctx)
	})
	r := newLiveTimingReceiver(connectionTestConfig(t, server.URL), receivertest.NewNopSettings(Type))
	r.retryDelay = func(int) time.Duration { return 0 }
	ticks := make(chan time.Time, 1)
	r.newRacingTicker = func() (<-chan time.Time, func()) { return ticks, func() {} }
	r.state = identityGateTestState()
	r.state.drivers = fieldRoster()
	r.state.timing.drivers = map[int64]timingDriver{1: {driverRegistryEntry: r.state.drivers.drivers[0], position: 1}}
	r.now = func() time.Time { return fieldTime(100) }
	r.state.origin = fieldTime(1)
	m, err := consumer.NewMetrics(func(cb context.Context, _ pmetric.Metrics) error { close(blocked); <-cb.Done(); return cb.Err() })
	if err != nil {
		t.Fatal(err)
	}
	r.registerMetrics(m)
	if err := r.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(context.Background())
	ticks <- fieldTime(100)
	select {
	case <-reconnected:
	case <-ctx.Done():
		t.Fatal("timer consumer prevented reconnect")
	}
}

func TestRacingDiagnosticsUseStaticMessagesOnce(t *testing.T) {
	core, entries := observer.New(zap.WarnLevel)
	settings := receivertest.NewNopSettings(Type)
	r := newLiveTimingReceiver(nil, settings)
	r.operational = &operationalReporter{logger: zap.New(core)}
	r.state.idCollision = true
	r.state.timing.control.repairOverflow = map[int64]bool{7: true}
	s := &r.state.timing
	s.deltaStart = fieldTime(1)
	s.streamTimes = map[string]time.Time{"private instrument": fieldTime(11)}
	s.observe("private instrument", sessionFixture().identity, fieldTime(3), 1, nil)
	for range 3 {
		r.deliver(t.Context(), liveTimingEffects{})
	}
	want := []string{"F1 span ID collision; affected span subtree suppressed", "F1 late metric observation excluded after window finalization", "F1 session record bound reached; excess records suppressed"}
	if entries.Len() != len(want) {
		t.Fatal(entries.All())
	}
	for i, entry := range entries.All() {
		if entry.Message != want[i] || len(entry.Context) != 0 || entry.Level != zap.WarnLevel {
			t.Fatal(entry)
		}
	}
}

func TestGraceExpiresDuringReconnect(t *testing.T) {
	for _, setup := range []bool{false, true} {
		t.Run(fmt.Sprintf("during_setup=%t", setup), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ready, connecting := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			server := newConnectionTestServer(t, func(c *websocket.Conn) {
				_, _, _ = c.Read(ctx)
				if calls.Add(1) == 2 {
					close(connecting)
					_, _, _ = c.Read(ctx)
					return
				}
				_ = c.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
				_, _, _ = c.Read(ctx)
			})
			r := newLiveTimingReceiver(connectionTestConfig(t, server.URL), receivertest.NewNopSettings(Type))
			r.retryDelay = func(int) time.Duration {
				if setup {
					return 0
				}
				return time.Hour
			}
			o := sessionFixture()
			r.state = identityGateTestState()
			r.state.origin = o.observed
			r.state.sessionInfo.identity = o.identity
			r.state.sessionInfo.synchronized = false
			r.state.drivers = fieldRoster()
			r.state.timing, _ = applySessionStatusTransition(timingStart(o), o, sessionStatusTransition{current: "Finalised", utc: fieldTime(12)}, r.state.drivers)
			r.now = func() time.Time { return o.observed.Add(5 * time.Minute) }
			ticks := make(chan time.Time)
			var tickers int
			r.newRacingTicker = func() (<-chan time.Time, func()) {
				tickers++
				if tickers == 2 {
					close(ready)
				}
				return ticks, func() {}
			}
			delivered := make(chan ptrace.Traces, 1)
			tr, err := consumer.NewTraces(func(_ context.Context, p ptrace.Traces) error { delivered <- p; return nil })
			if err != nil {
				t.Fatal(err)
			}
			r.registerTraces(tr)
			if err = r.Start(ctx, nil); err != nil {
				t.Fatal(err)
			}
			defer r.Shutdown(context.Background())
			stage := ready
			if setup {
				stage = connecting
			}
			select {
			case <-stage:
			case <-ctx.Done():
				t.Fatal("did not reach reconnect stage")
			}
			select {
			case ticks <- r.now():
			case <-ctx.Done():
				t.Fatal("tick not serviced")
			}
			select {
			case batch := <-delivered:
				if batch.SpanCount() != 2 {
					t.Fatal(batch.SpanCount())
				}
				for _, span := range batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().All() {
					if span.EndTimestamp() != 12e9 {
						t.Fatal(span.EndTimestamp())
					}
				}
			case <-ctx.Done():
				t.Fatal("no grace closure")
			}
		})
	}
}

// One driver completes a lap and makes a pit visit. The radio index arrives
// after Ends but its capture belongs to the exported driver interval.
func projectionSession(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"type":3,"invocationId":"0","result":{"DriverList":{"1":{"Tla":"ABC","RacingNumber":"1","FullName":"Example Driver","TeamName":"Example Team"}},"SessionInfo":` + identityGateDescriptorB + `}}` + "\x1e")
	feed := func(sec int64, topic, p string) {
		fmt.Fprintf(&b, `{"type":1,"target":"feed","arguments":[%q,%s,%q]}`+"\x1e", topic, p, fieldTime(sec).Format(time.RFC3339))
	}
	status := func(index, sec int64, k, v string) {
		feed(sec, "SessionData", fmt.Sprintf(`{"StatusSeries":{"%d":{"Utc":%q,%q:%q}}}`, index, fieldTime(sec).Format(time.RFC3339), k, v))
	}
	line := func(sec int64, fields string) { feed(sec, "TimingData", `{"Lines":{"1":{`+fields+`}}}`) }
	status(0, 1, "SessionStatus", "Started")
	feed(2, "LapCount", `{"CurrentLap":1,"TotalLaps":2}`)
	feed(2, "ExtrapolatedClock", `{"Utc":"1970-01-01T00:00:02Z","Remaining":"00:02:00","Extrapolating":false}`)
	feed(3, "WeatherData", `{"Rainfall":"0"}`)
	feed(4, "WeatherData", `{"Rainfall":"1"}`)
	feed(5, "TimingAppData", `{"Lines":{"1":{"Stints":{"0":{"Compound":"Soft","TotalLaps":0}}}}}`)
	feed(5, "TimingStats", `{"Lines":{"1":{"BestSpeeds":{"I1":{"Value":"299"}}}}}`)
	feed(8, "CarData.z", string(compressedJSONPayload(t, []byte(`{"Entries":[`+carRow(fieldTime(8), fieldCar(1, `"2":250`))+`]}`))))
	line(10, `"Position":"1","Stopped":false,"NumberOfLaps":1,"InPit":false`)
	status(1, 11, "TrackStatus", "Yellow")
	feed(12, "Position.z", string(compressedJSONPayload(t, []byte(`{"Position":[{"Timestamp":"1970-01-01T00:00:12Z","Entries":{"1":{"X":10,"Y":20,"Z":30}}}]}`))))
	feed(12, "RaceControlMessages", `{"Messages":{"0":{"Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"1","Message":"blue"},"1":{"Category":"CarEvent","RacingNumber":"1","Message":"notice"}}}`)
	line(30, `"Sectors":{"0":{"Value":"20.000"}}`)
	line(50, `"Sectors":{"1":{"Value":"20.000"}}`)
	line(70, `"NumberOfLaps":2,"InPit":true,"NumberOfPitStops":1,"LastLapTime":{"Value":"1:00.000","PersonalFastest":true},"Speeds":{"ST":{"Value":"310"},"FL":{"Value":"300"}}`)
	line(74, `"Sectors":{"2":{"Value":"20.000"}},"InPit":false`)
	feed(75, "PitLaneTimeCollection", `{"PitTimes":{"1":{"Lap":"2","Duration":"21.4"}}}`)
	feed(75, "PitStop", `{"RacingNumber":"1","Lap":"2","PitStopTime":"2.3","PitLaneTime":"21.499"}`)
	status(2, 80, "SessionStatus", "Finalised")
	status(3, 81, "SessionStatus", "Ends")
	feed(82, "TeamRadio", `{"Captures":{"0":{"Utc":"1970-01-01T00:00:40Z","RacingNumber":"1","Path":"TeamRadio/ABC_1_20220101_000040.mp3"},"1":{"RacingNumber":"1","Path":"https://private.invalid/?token=synthetic-secret"}}}`)
	b.WriteString(incrementalClose)
	return b.String()
}

func TestProjectionRuntime(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "waterfall", true: "failing traces"}[fail], func(t *testing.T) {
			wire := projectionSession(t)
			var connections, failures atomic.Int32
			server := newConnectionTestServer(t, func(socket *websocket.Conn) {
				connections.Add(1)
				_, _, _ = socket.Read(t.Context())
				_ = socket.Write(t.Context(), websocket.MessageText, []byte("{}\x1e"))
				_, _, _ = socket.Read(t.Context())
				_ = socket.Write(t.Context(), websocket.MessageText, []byte(wire))
			})
			f := NewFactory()
			cfg := connectionTestConfig(t, server.URL)
			settings := receivertest.NewNopSettings(Type)
			ts, ms, ls := &consumertest.TracesSink{}, &consumertest.MetricsSink{}, &consumertest.LogsSink{}
			var next consumer.Traces = ts
			if fail {
				var err error
				next, err = consumer.NewTraces(func(context.Context, ptrace.Traces) error { failures.Add(1); return errors.New("synthetic failure") })
				if err != nil {
					t.Fatal(err)
				}
			}
			c, err := f.CreateTraces(t.Context(), settings, cfg, next)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.CreateMetrics(t.Context(), settings, cfg, ms); err != nil {
				t.Fatal(err)
			}
			if _, err = f.CreateLogs(t.Context(), settings, cfg, ls); err != nil {
				t.Fatal(err)
			}
			if err = c.Start(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
			r := c.(*sharedReceiver).receiver
			select {
			case <-r.done:
			case <-time.After(5 * time.Second):
				t.Fatal("source did not close")
			}
			if connections.Load() != 1 || r.operational.state.Load().attempts != 0 || r.operational.state.Load().consumerFailures != int64(failures.Load()) || fail && failures.Load() == 0 {
				t.Fatal("consumer failure accounting or reconnect", failures.Load())
			}
			check := func(b []byte, err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"subscription-token", "affinity-token", "connection-token", "private.invalid", "synthetic-secret", server.URL, "https://", "http://"} {
					if strings.Contains(string(b), secret) {
						t.Fatal("credential or URL in OTLP")
					}
				}
			}
			id := r.state.sessionInfo.identity
			root := deterministicRootSpanID(id, 1)
			bodies := map[string]int{}
			for _, batch := range ls.AllLogs() {
				check((&plog.JSONMarshaler{}).MarshalLogs(batch))
				for _, p := range batch.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().All() {
					bodies[p.Body().Str()]++
					if p.Body().Str() == "f1.team_radio.available" && (p.SpanID() != root || p.TraceID() != deterministicTraceID(id, 1)) {
						t.Fatal("uncorrelated radio")
					}
				}
			}
			for body, n := range map[string]int{"f1.session.status.changed": 3, "f1.track_status.changed": 1, "f1.weather.rainfall.changed": 1, "blue": 1, "notice": 1, "f1.team_radio.available": 1} {
				if bodies[body] != n {
					t.Fatal(body, bodies)
				}
			}
			names := map[string]bool{}
			for _, batch := range ms.AllMetrics() {
				check((&pmetric.JSONMarshaler{}).MarshalMetrics(batch))
				for _, m := range batch.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().All() {
					names[m.Name()] = true
				}
			}
			for _, name := range []string{currentLapMetric, totalLapsMetric, clockMetric, "f1.field.speed", "f1.driver.blue_flags", "f1.driver.last_lap_time", "f1.driver.sector_time", "f1.driver.lap_time", "f1.pit.stop_duration", "f1.pit.lane_duration", "f1.driver.best_speed_trap", "f1.driver.tyre.age"} {
				if !names[name] {
					t.Fatal(name, names)
				}
			}
			if fail {
				return
			}
			stint := func(n int64) pcommon.SpanID { return deterministicStintSpanID(id, 1, "none", n) }
			lap := func(n int64) pcommon.SpanID { return deterministicLapSpanID(id, 1, "none", n) }
			parents := map[pcommon.SpanID]pcommon.SpanID{root: {}, stint(1): root, stint(2): root, lap(1): stint(1), lap(2): stint(2)}
			for i := int64(1); i <= 3; i++ {
				parents[deterministicSectorSpanID(id, 1, "none", 1, i)] = lap(1)
			}
			events := map[string]bool{}
			for _, batch := range ts.AllTraces() {
				check((&ptrace.JSONMarshaler{}).MarshalTraces(batch))
				for _, s := range batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().All() {
					parent, ok := parents[s.SpanID()]
					if !ok || s.ParentSpanID() != parent || s.TraceID() != deterministicTraceID(id, 1) {
						t.Fatal(s.Name(), s.ParentSpanID())
					}
					delete(parents, s.SpanID())
					if s.SpanID() == root {
						for k, want := range map[string]string{"f1.meeting.name": "Example Grand Prix", "f1.driver.name": "Example Driver", "f1.constructor.name": "Example Team"} {
							if v, ok := s.Attributes().Get(k); !ok || v.Str() != want {
								t.Fatal(k, want)
							}
						}
					}
					for _, ev := range s.Events().All() {
						events[ev.Name()] = true
						if ev.Name() == "f1.blue_flag.shown" {
							if x, ok := ev.Attributes().Get("f1.position.x"); !ok || x.Double() != 1 {
								t.Fatal("missing Position.z enrichment")
							}
						}
					}
				}
			}
			if len(parents) > 0 {
				t.Fatal("missing spans", parents)
			}
			for _, name := range []string{"f1.track_status.changed", "f1.personal_best", "f1.pit.entry", "f1.pit.stop", "f1.blue_flag.shown", "f1.race_control.driver_notice"} {
				if !events[name] {
					t.Fatal(name, events)
				}
			}
		})
	}
}

func TestCancellationDeliversOwnedRoot(t *testing.T) {
	wire := projectionSession(t)
	// Stop the source after the first Started record.
	first := strings.SplitN(wire, "\x1e", 3)
	server := newConnectionTestServer(t, func(socket *websocket.Conn) {
		_, _, _ = socket.Read(t.Context())
		_ = socket.Write(t.Context(), websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = socket.Read(t.Context())
		_ = socket.Write(t.Context(), websocket.MessageText, []byte(strings.Join(first[:2], "\x1e")+"\x1e"))
		_, _, _ = socket.Read(t.Context())
	})
	r := newLiveTimingReceiver(connectionTestConfig(t, server.URL), receivertest.NewNopSettings(Type))
	calls := 0
	tr, err := consumer.NewTraces(func(delivery context.Context, batch ptrace.Traces) error {
		calls++
		span := batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
		if err := delivery.Err(); err != nil || batch.SpanCount() != 1 || span.Name() != "driver.session ABC" || span.StartTimestamp() != 1e9 || span.EndTimestamp() != 1e9 {
			t.Errorf("terminal delivery: %v, %s [%v,%v]", err, span.Name(), span.StartTimestamp(), span.EndTimestamp())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.registerTraces(tr)
	started := make(chan struct{})
	r.consume = func(_ context.Context, b normalizedLiveTimingBatch) error {
		if b.source == liveTimingUpdateSourceFeed {
			close(started)
		}
		return nil
	}
	if err := r.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("no Started transition")
	}
	if err := r.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("shutdown trace deliveries", calls)
	}
}

func TestProjectionRuntimeCircuitEnrichment(t *testing.T) {
	sessionWithCircuit := strings.Replace(identityGateDescriptorB, `"Meeting":{"Key":1200,`, `"Meeting":{"Key":1200,"Circuit":{"Key":77,"ShortName":"Synthetic Ring"},`, 1)
	fullWire := strings.Replace(projectionSession(t), identityGateDescriptorB, sessionWithCircuit, 1)
	snapshotWire, feedWire, ok := strings.Cut(fullWire, "\x1e")
	if !ok {
		t.Fatal("missing snapshot separator")
	}

	for _, mode := range []string{"enriched", "unavailable fallback", "stalled nonblocking", "duplicate circuit key"} {
		t.Run(mode, func(t *testing.T) {
			var circuitHits atomic.Int32
			handlerEntered := make(chan struct{})
			var enteredOnce sync.Once
			releaseStall := make(chan struct{})
			defer func() {
				select {
				case <-releaseStall:
				default:
					close(releaseStall)
				}
			}()
			circuitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				circuitHits.Add(1)
				enteredOnce.Do(func() { close(handlerEntered) })
				if req.URL.Path != "/77/2022" || req.Header.Get("Authorization") != "" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				switch mode {
				case "unavailable fallback":
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				case "stalled nonblocking":
					<-releaseStall
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"circuitKey":77,"year":2022,"corners":[{"number":4,"letter":"A","trackPosition":{"x":10,"y":20}}],"marshalSectors":[{"number":8,"trackPosition":{"x":10,"y":20}}]}`))
			}))
			t.Cleanup(circuitServer.Close)

			snap := snapshotWire
			if mode == "duplicate circuit key" {
				snap = strings.Replace(snapshotWire, `"Key":77`, `"Key":77,"Key":78`, 1)
			}
			settled := make(chan struct{})
			server := newConnectionTestServer(t, func(socket *websocket.Conn) {
				_, _, _ = socket.Read(t.Context())
				_ = socket.Write(t.Context(), websocket.MessageText, []byte("{}\x1e"))
				_, _, _ = socket.Read(t.Context())
				_ = socket.Write(t.Context(), websocket.MessageText, []byte(snap+"\x1e"))
				switch mode {
				case "enriched", "unavailable fallback":
					select {
					case <-settled:
					case <-t.Context().Done():
						return
					}
				case "stalled nonblocking":
					select {
					case <-handlerEntered:
					case <-t.Context().Done():
						return
					}
				}
				_ = socket.Write(t.Context(), websocket.MessageText, []byte(feedWire))
			})
			f := NewFactory()
			cfg := connectionTestConfig(t, server.URL)
			cfg.CircuitEnrichment = CircuitEnrichmentConfig{Enabled: true, Endpoint: circuitServer.URL}
			core, recorded := observer.New(zap.WarnLevel)
			settings := receivertest.NewNopSettings(Type)
			settings.Logger = zap.New(core)
			ts := &consumertest.TracesSink{}
			c, err := f.CreateTraces(t.Context(), settings, cfg, ts)
			if err != nil {
				t.Fatal(err)
			}
			var settleOnce sync.Once
			c.(*sharedReceiver).receiver.onCircuitSettled = func() {
				settleOnce.Do(func() { close(settled) })
			}
			if err := c.Start(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
			select {
			case <-c.(*sharedReceiver).receiver.done:
			case <-time.After(5 * time.Second):
				t.Fatal("source did not close")
			}
			wantHits := int32(1)
			if mode == "duplicate circuit key" {
				wantHits = 0
			}
			if circuitHits.Load() != wantHits {
				t.Fatalf("circuit requests = %d, want %d", circuitHits.Load(), wantHits)
			}
			foundBlue := false
			wantEnriched := mode == "enriched"
			for _, batch := range ts.AllTraces() {
				raw, err := (&ptrace.JSONMarshaler{}).MarshalTraces(batch)
				if err != nil || strings.Contains(string(raw), circuitServer.URL) {
					t.Fatal("marshal error or URL leak", err)
				}
				for _, s := range batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().All() {
					for _, ev := range s.Events().All() {
						if ev.Name() != "f1.blue_flag.shown" {
							continue
						}
						foundBlue = true
						corner, hasCorner := ev.Attributes().Get("f1.circuit.corner.label")
						sector, hasSector := ev.Attributes().Get("f1.circuit.marshal_sector")
						if wantEnriched && (!hasCorner || corner.Str() != "4A" || !hasSector || sector.Int() != 8) {
							t.Fatalf("missing enrichment: %v", ev.Attributes().AsRaw())
						}
						if !wantEnriched && (hasCorner || hasSector) {
							t.Fatalf("unexpected enrichment: %v", ev.Attributes().AsRaw())
						}
					}
				}
			}
			if !foundBlue {
				t.Fatal("missing f1.blue_flag.shown event")
			}
			warns := recorded.FilterMessage("F1 circuit geometry unavailable; event enrichment skipped").All()
			wantWarns := 0
			if mode == "unavailable fallback" {
				wantWarns = 1
			}
			if len(warns) != wantWarns {
				t.Fatalf("circuit warnings = %d, want %d", len(warns), wantWarns)
			}
		})
	}
}
