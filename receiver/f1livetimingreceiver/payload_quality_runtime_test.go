package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/receiver/receivertest"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// Independent exact notice oracle: no payload-derived fields or category labels.
const wantPayloadQualityMessage = "Live Timing payload contains malformed Unicode scalar escapes; normalized payload bytes preserved; no F1 race export is implemented"

func assertPayloadQualityWarnings(t *testing.T, logs *observer.ObservedLogs, totals, deltas []int64) {
	t.Helper()
	warnings := logs.FilterMessage(wantPayloadQualityMessage).All()
	if len(warnings) != len(totals) || len(totals) != len(deltas) {
		t.Fatalf("quality warnings=%+v; want totals=%v deltas=%v", warnings, totals, deltas)
	}
	for i, warning := range warnings {
		want := map[string]any{"invalid_unicode_updates": totals[i], "new_invalid_unicode_updates": deltas[i]}
		if warning.Level != zap.WarnLevel || !reflect.DeepEqual(warning.ContextMap(), want) {
			t.Fatalf("warning=%+v; want fields=%v", warning, want)
		}
	}
}

func TestPayloadQualityRuntimeCadenceAndSummary(t *testing.T) {
	for _, metricsEnabled := range []bool{false, true} {
		name := "metrics-None"
		if metricsEnabled {
			name = "metrics-enabled"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reader := sdkmetric.NewManualReader()
				provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
				defer func() { _ = provider.Shutdown(t.Context()) }()
				settings := receivertest.NewNopSettings(Type)
				settings.ID = component.NewIDWithName(Type, "quality")
				core, logs := observer.New(zap.InfoLevel)
				settings.Logger = zap.New(core)
				if metricsEnabled {
					settings.MeterProvider = provider
				}
				r := newLiveTimingReceiver(nil, settings) // Keep the production no-op consumer.
				var err error
				r.operational, err = newOperationalReporter(t.Context(), settings)
				if err != nil {
					t.Fatal(err)
				}
				host := &statusHost{events: make(chan *componentstatus.Event, 8)}
				origin := time.Now()
				r.operational.start(host)
				requireAwaitingInput(t, host)
				ctx, cancel := context.WithCancel(t.Context())
				r.cancel, r.done = cancel, make(chan struct{})
				socket := newLivenessSocket()
				connection := &signalRConnection{conn: socket}
				if err := connection.subscribe(ctx); err != nil {
					t.Fatal(err)
				}
				go r.run(ctx, connection, r.done)
				defer func() { _ = r.Shutdown(context.Background()) }()
				send := func(record string) { socket.reads <- livenessRead{contents: record}; synctest.Wait() }
				advance := func(d time.Duration) { time.Sleep(d); synctest.Wait() }
				const affected = `{"unused-secret-key":"synthetic-private-text\uD800","nested":["\uDC00","\uDFFF"]}`
				inflated := compressedJSONPayload(t, []byte(affected))
				feed := `{"type":1,"target":"feed","arguments":["Unused",` + affected + `,"2026-08-21T10:30:30Z"]}` + "\x1e"
				advance(10 * time.Second)
				send(hubPingRecord)
				advance(10 * time.Second)
				send(hubPingRecord)
				advance(9 * time.Second)
				// First quality notice accompanies a fully accepted nonempty snapshot.
				send(`{"type":3,"invocationId":"0","result":{"SessionInfo":` + affected + `,"CarData.z":` + string(inflated) + `,"Heartbeat":{}}}` + "\x1e")
				assertPayloadQualityWarnings(t, logs, []int64{2}, []int64{2})
				if (<-host.events).Status() != componentstatus.StatusOK || logs.FilterMessageSnippet("First Live Timing").Len() != 1 {
					t.Fatal("quality finding displaced first-data readiness")
				}
				// A/affected-B/C in one frame continues through C, counting B only once.
				send(incrementalFeedA + feed + incrementalFeedC)
				assertPayloadQualityWarnings(t, logs, []int64{2}, []int64{2})
				want := operationalState{started: origin, connection: true, subscription: true, connectionData: true, ready: true, updates: 6, lastUpdate: time.Now(), invalidUnicodeUpdates: 3, reportedInvalidUnicodeUpdates: 2}
				if *r.operational.state.Load() != want {
					t.Fatalf("accepted state=%+v; want %+v", r.operational.state.Load(), want)
				}
				advance(time.Second - time.Nanosecond)
				assertPayloadQualityWarnings(t, logs, []int64{2}, []int64{2})
				advance(time.Nanosecond) // Existing tick at t=30, not 30 seconds after first finding.
				assertPayloadQualityWarnings(t, logs, []int64{2, 3}, []int64{2, 1})
				send(feed) // After the tick: pending until t=60.
				r.operational.apply(operationalInput{event: opTick})
				assertPayloadQualityWarnings(t, logs, []int64{2, 3}, []int64{2, 1})
				for range 2 {
					advance(10 * time.Second)
					send(hubPingRecord)
				}
				advance(10*time.Second - time.Nanosecond)
				assertPayloadQualityWarnings(t, logs, []int64{2, 3}, []int64{2, 1})
				advance(time.Nanosecond)
				assertPayloadQualityWarnings(t, logs, []int64{2, 3, 4}, []int64{2, 1, 1})
				send(incrementalFeedC)
				for range 2 {
					advance(10 * time.Second)
					send(hubPingRecord)
				}
				advance(10 * time.Second)
				assertPayloadQualityWarnings(t, logs, []int64{2, 3, 4}, []int64{2, 1, 1})
				send(feed) // Shutdown must retain this unreported finding in its summary.
				want.updates, want.lastUpdate = 9, time.Now()
				want.invalidUnicodeUpdates, want.reportedInvalidUnicodeUpdates = 5, 4
				if *r.operational.state.Load() != want || len(host.events) != 0 {
					t.Fatalf("quality changed transport state/status: %+v", r.operational.state.Load())
				}
				wantMetrics := map[string]metricResult{}
				if metricsEnabled {
					wantMetrics = zeroOperationalMetrics("f1livetiming/quality")
					for name, value := range map[string]float64{"connection_active": 1, "subscription_active": 1, "normalized_updates": 9, "invalid_unicode_updates": 5} {
						wantMetrics[metricPrefix+name].Points[0].Value = value
					}
					wantMetrics[metricPrefix+"last_update_age"] = metricResult{"s", "Float64Gauge", []metricPoint{{"f1livetiming/quality", 0}}}
				}
				for range 2 {
					if got := collectOperational(t, reader); !reflect.DeepEqual(got, wantMetrics) || *r.operational.state.Load() != want {
						t.Fatalf("metrics=%#v; want %#v", got, wantMetrics)
					}
				}
				if err := r.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
				want.connection, want.subscription, want.connectionData, want.stopped, want.summarized = false, false, false, true, true
				if *r.operational.state.Load() != want {
					t.Fatalf("final state=%+v; want %+v", r.operational.state.Load(), want)
				}
				summary := logs.FilterMessageSnippet("interruption summary").All()
				wantSummary := map[string]any{"attempt": int64(0), "run_elapsed_seconds": float64(90), "outage_duration_seconds": float64(0), "total_outage_duration_seconds": float64(0), "next_delay_seconds": float64(0),
					"connection_active": false, "subscription_active": false, "normalized_updates": int64(9), "outages": int64(0), "recoveries": int64(0), "consumer_failures": int64(0), "unresolved_outage": false, "invalid_unicode_updates": int64(5)}
				if len(summary) != 1 || summary[0].Level != zap.InfoLevel || !reflect.DeepEqual(summary[0].ContextMap(), wantSummary) {
					t.Fatalf("summary=%+v; want fields=%v", summary, wantSummary)
				}
				assertPayloadQualityWarnings(t, logs, []int64{2, 3, 4}, []int64{2, 1, 1})
				// Exact complete log inventory forbids raw-text or semantic-recovery extras.
				if logs.Len() != 6 {
					t.Fatalf("unexpected output: %+v", logs.All())
				}
				advance(time.Minute)
				if logs.Len() != 6 {
					t.Fatal("reporter survived shutdown")
				}
			})
		})
	}
}

func TestPayloadQualityRuntimeRejectsWholeSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := &statusHost{events: make(chan *componentstatus.Event, 8)}
		r, socket, logs := operationalTestRun(t, host)
		requireAwaitingInput(t, host)
		origin := time.Now()
		var delivered []normalizedLiveTimingBatch
		r.consume = func(_ context.Context, batch normalizedLiveTimingBatch) error {
			delivered = append(delivered, batch)
			return nil
		}
		r.now = func() time.Time { return origin }
		socket.reads <- livenessRead{contents: incrementalFeedA + `{"type":3,"invocationId":"0","result":{"SessionInfo":{"unused":"\uD800"},"CarData.z":"AAAA"}}` + "\x1e" + incrementalFeedC}
		<-r.done
		want := operationalState{started: origin, outageStarted: origin, lastUpdate: origin, outage: true, stopped: true, summarized: true, outages: 1, updates: 1}
		wantBatch := normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed, requestedTopics: []string{}, presentTopics: []string{}, observationTime: origin,
			updates: []normalizedLiveTimingUpdate{{topic: "SessionStatus", payload: []byte(`{"Status":"Started"}`), timestamp: time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC), source: liveTimingUpdateSourceFeed}}}
		if *r.operational.state.Load() != want || !reflect.DeepEqual(delivered, []normalizedLiveTimingBatch{wantBatch}) {
			t.Fatalf("state=%+v delivered=%#v; want %+v / %#v", r.operational.state.Load(), delivered, want, wantBatch)
		}
		assertPayloadQualityWarnings(t, logs, nil, nil)
		if (<-host.events).Status() != componentstatus.StatusPermanentError || len(host.events) != 0 || logs.FilterMessage(errPermanentLiveTimingFailure.Error()).Len() != 1 {
			t.Fatal("rejected normalization changed terminal failure policy")
		}
	})
}

func TestPayloadQualityRuntimeTerminalSummary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := &statusHost{events: make(chan *componentstatus.Event, 8)}
		r, socket, logs := operationalTestRun(t, host)
		requireAwaitingInput(t, host)
		origin := time.Now()
		feed := `{"type":1,"target":"feed","arguments":["Unused",{"unused":"\uD800"},"2026-08-21T10:30:00Z"]}` + "\x1e"
		socket.reads <- livenessRead{contents: feed + feed + incrementalClose}
		<-r.done
		want := operationalState{started: origin, outageStarted: origin, lastUpdate: origin, outage: true, stopped: true, summarized: true, outages: 1, updates: 2,
			invalidUnicodeUpdates: 2, reportedInvalidUnicodeUpdates: 1}
		if *r.operational.state.Load() != want {
			t.Fatalf("terminal state=%+v; want %+v", r.operational.state.Load(), want)
		}
		assertPayloadQualityWarnings(t, logs, []int64{1}, []int64{1})
		wantSummary := map[string]any{"attempt": int64(0), "run_elapsed_seconds": float64(0), "outage_duration_seconds": float64(0), "total_outage_duration_seconds": float64(0), "next_delay_seconds": float64(0),
			"connection_active": false, "subscription_active": false, "normalized_updates": int64(2), "outages": int64(1), "recoveries": int64(0), "consumer_failures": int64(0), "unresolved_outage": true, "invalid_unicode_updates": int64(2)}
		summary := logs.FilterMessageSnippet("interruption summary").All()
		if len(summary) != 1 || !reflect.DeepEqual(summary[0].ContextMap(), wantSummary) || logs.FilterMessage(errSourceStopped.Error()).Len() != 1 || logs.Len() != 4 {
			t.Fatalf("terminal output=%+v", logs.All())
		}
		if (<-host.events).Status() != componentstatus.StatusPermanentError || len(host.events) != 0 {
			t.Fatal("quality findings changed component status")
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		assertPayloadQualityWarnings(t, logs, []int64{1}, []int64{1})
		if *r.operational.state.Load() != want || logs.FilterMessageSnippet("Live Timing input stopped;").Len() != 1 || logs.FilterMessageSnippet("interruption summary").Len() != 1 || logs.Len() != 5 {
			t.Fatalf("periodic reporting changed terminal quality result: %+v / %+v", r.operational.state.Load(), logs.All())
		}
	})
}

func TestPayloadQualitySummaryWaitsForConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, socket, logs := operationalTestRun(t, nil)
		origin := time.Now()
		entered, release := make(chan struct{}), make(chan struct{})
		r.consume = func(context.Context, normalizedLiveTimingBatch) error {
			close(entered)
			<-release
			return errors.New("synthetic private consumer failure")
		}
		socket.reads <- livenessRead{contents: `{"type":3,"invocationId":"0","result":{"SessionInfo":{"unused":"\uD800"}}}` + "\x1e"}
		<-entered
		want := operationalState{started: origin, lastUpdate: origin, connection: true, subscription: true, connectionData: true, ready: true, updates: 1, invalidUnicodeUpdates: 1, reportedInvalidUnicodeUpdates: 1}
		assertPayloadQualityWarnings(t, logs, []int64{1}, []int64{1})
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := r.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) || *r.operational.state.Load() != want || logs.FilterMessageSnippet("interruption summary").Len() != 0 {
			t.Fatalf("premature summary/state change: err=%v state=%+v", err, r.operational.state.Load())
		}
		close(release)
		<-r.done
		want.connection, want.subscription, want.connectionData, want.stopped, want.summarized = false, false, false, true, true
		want.consumerFailures = 1
		if *r.operational.state.Load() != want {
			t.Fatalf("consumer completion=%+v; want %+v", r.operational.state.Load(), want)
		}
		wantSummary := map[string]any{"attempt": int64(0), "run_elapsed_seconds": float64(1), "outage_duration_seconds": float64(0), "total_outage_duration_seconds": float64(0), "next_delay_seconds": float64(0),
			"connection_active": false, "subscription_active": false, "normalized_updates": int64(1), "outages": int64(0), "recoveries": int64(0), "consumer_failures": int64(1), "unresolved_outage": false, "invalid_unicode_updates": int64(1)}
		summary := logs.FilterMessageSnippet("interruption summary").All()
		consumer := logs.FilterMessage("F1 live timing batch consumer failed; input observation does not imply export").All()
		if len(summary) != 1 || !reflect.DeepEqual(summary[0].ContextMap(), wantSummary) || len(consumer) != 1 || len(consumer[0].Context) != 0 || logs.Len() != 5 {
			t.Fatalf("unexpected completion output: %+v", logs.All())
		}
	})
}

func TestPayloadQualityDistinctInputsHaveFixedCardinality(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		defer func() { _ = provider.Shutdown(t.Context()) }()
		settings := receivertest.NewNopSettings(Type)
		settings.ID, settings.MeterProvider = component.NewID(Type), provider
		core, logs := observer.New(zap.InfoLevel)
		settings.Logger = zap.New(core)
		r, err := newOperationalReporter(t.Context(), settings)
		if err != nil {
			t.Fatal(err)
		}
		defer r.stop(t.Context())
		origin := time.Now()
		r.apply(operationalInput{event: opStart})
		r.apply(operationalInput{event: opConnected})
		for i := range 128 {
			// Vary topics, nested keys, and text independently; none is a label.
			batch, err := normalizeLiveTimingBatch(liveTimingBatch{source: liveTimingUpdateSourceFeed, updates: []liveTimingUpdate{{
				topic: fmt.Sprintf("Unused%d", i), payload: []byte(fmt.Sprintf(`{"key%d":{"nested%d":"text%d\uD800","\uDC00":null}}`, i, i*2, i*3)),
				timestamp: "2026-08-21T10:30:00Z", source: liveTimingUpdateSourceFeed,
			}}}, origin)
			if err != nil {
				t.Fatal(err)
			}
			r.apply(operationalInput{event: opBatch, updates: len(batch.updates), invalidUnicodeUpdates: batch.invalidUnicodeUpdates})
		}
		assertPayloadQualityWarnings(t, logs, []int64{1}, []int64{1})
		r.apply(operationalInput{event: opPeriodicTick})
		assertPayloadQualityWarnings(t, logs, []int64{1, 128}, []int64{1, 127})
		want := operationalState{started: origin, lastUpdate: origin, connection: true, connectionData: true, updates: 128, invalidUnicodeUpdates: 128, reportedInvalidUnicodeUpdates: 128}
		if *r.state.Load() != want {
			t.Fatalf("state=%+v; want %+v", r.state.Load(), want)
		}
		wantMetrics := zeroOperationalMetrics("f1livetiming")
		wantMetrics[metricPrefix+"connection_active"].Points[0].Value = 1
		wantMetrics[metricPrefix+"normalized_updates"].Points[0].Value = 128
		wantMetrics[metricPrefix+"invalid_unicode_updates"].Points[0].Value = 128
		wantMetrics[metricPrefix+"last_update_age"] = metricResult{"s", "Float64Gauge", []metricPoint{{"f1livetiming", 0}}}
		if got := collectOperational(t, reader); !reflect.DeepEqual(got, wantMetrics) {
			t.Fatalf("metrics=%#v; want %#v", got, wantMetrics)
		}
	})
}
