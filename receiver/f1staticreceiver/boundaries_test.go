package f1staticreceiver

import (
	"context"
	"fmt"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver"
	"slices"
	"testing"
	"time"
)

func TestReplayGraceDeadline(t *testing.T) {
	origin := time.Date(2025, 3, 10, 10, 0, 0, 0, time.UTC)
	deadline := 301 * time.Second
	for _, skew := range []time.Duration{-1, 0, 1} {
		t.Run(skew.String(), func(t *testing.T) {
			end := origin.Add(deadline + skew)
			p, err := freeze([]byte(info), []byte(drivers), map[string][]StreamRecord{
				"SessionData": {{0, `{"StatusSeries":[{"SessionStatus":"Started","Utc":"2025-03-10T10:00:00Z"}]}`}, {1e9, `{"StatusSeries":{"1":{"SessionStatus":"Finalised","Utc":"2025-03-10T10:00:01Z"}}}`}, {int64(deadline + skew), fmt.Sprintf(`{"StatusSeries":{"2":{"SessionStatus":"Ends","Utc":%q}}}`, end.Format(time.RFC3339Nano))}},
				// This batch precedes SessionData at the same coordinate.
				"Heartbeat": {{0, `{"Utc":"2025-03-10T10:00:00Z"}`}, {int64(deadline + skew), `{}`}}})
			if err != nil {
				t.Fatal(err)
			}
			r := newReceiver(DefaultConfig(), receiver.Settings{})
			var got []time.Time
			r.traces, _ = consumer.NewTraces(func(_ context.Context, tr ptrace.Traces) error {
				for _, s := range tr.ResourceSpans().At(0).ScopeSpans().At(0).Spans().All() {
					got = append(got, s.EndTimestamp().AsTime().UTC())
				}
				return nil
			})
			if err := r.play(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			want := end
			if skew > 0 {
				want = origin.Add(time.Second)
			}
			if !slices.Equal(got, []time.Time{want}) {
				t.Fatalf("root ends %v, want %v", got, want)
			}
		})
	}
}
func TestReplayRadioArrayAndSparseUpdates(t *testing.T) {
	const capture = `{"Path":"TeamRadio/FIG_17_20250310_100000.mp3","RacingNumber":"17"}`
	p, err := freeze([]byte(info), []byte(drivers), map[string][]StreamRecord{"Heartbeat": {{0, `{"Utc":"2025-03-10T10:00:00Z"}`}}, "TeamRadio": {{0, `{"Captures":[` + capture + `,{},` + capture + `]}`}, {1e9, `{"Captures":[{},{},{},` + capture + `]}`}, {2e9, `{"Captures":{"1":` + capture + `,"4":` + capture + `}}`}}})
	if err != nil {
		t.Fatal(err)
	}
	r := newReceiver(DefaultConfig(), receiver.Settings{})
	var indexes []int64
	r.logs, _ = consumer.NewLogs(func(_ context.Context, l plog.Logs) error {
		for _, record := range l.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().All() {
			i, _ := record.Attributes().Get("f1.team_radio.index")
			indexes = append(indexes, i.Int())
		}
		return nil
	})
	if err := r.play(t.Context(), p); err != nil || !slices.Equal(indexes, []int64{0, 2, 4}) {
		t.Fatal(indexes, err)
	}
}

func TestReplayRejectsArrayAfterMalformedRadioOpeningRecord(t *testing.T) {
	const capture = `{"Path":"TeamRadio/FIG_17_20250310_100000.mp3","RacingNumber":"17"}`
	p, err := freeze([]byte(info), []byte(drivers), map[string][]StreamRecord{
		"Heartbeat": {{0, `{"Utc":"2025-03-10T10:00:00Z"}`}},
		"TeamRadio": {{0, `{"Captures":null}`}, {1e9, `{"Captures":[` + capture + `]}`}, {2e9, `{"Captures":{"0":` + capture + `}}`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newReceiver(DefaultConfig(), receiver.Settings{})
	var timestamps []time.Time
	r.logs, _ = consumer.NewLogs(func(_ context.Context, l plog.Logs) error {
		for _, record := range l.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().All() {
			timestamps = append(timestamps, record.Timestamp().AsTime().UTC())
		}
		return nil
	})
	want := []time.Time{time.Date(2025, 3, 10, 10, 0, 2, 0, time.UTC)}
	if err := r.play(t.Context(), p); err != nil || !slices.Equal(timestamps, want) {
		t.Fatal(timestamps, err)
	}
}
