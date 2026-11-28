package f1livetimingreceiver

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1engine"
)

const identityGateDescriptorA = `{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Key":101,"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00","EndDate":"2021-05-04T11:45:00","GmtOffset":"02:00:00"}`
const identityGateDescriptorB = `{"Meeting":{"Key":1200,"Name":"Example Grand Prix"},"Key":7000,"Type":"Race","Name":"Race","StartDate":"2022-01-01T12:00:00","EndDate":"2022-01-01T14:00:00","GmtOffset":"00:00:00"}`

func fieldTime(sec int64) time.Time { return time.Unix(sec, 0).UTC() }
func runtimeRoster(t *testing.T) f1engine.State {
	b := f1engine.Batch{Source: f1engine.Snapshot, ObservationTime: fieldTime(1)}
	for i, p := range []string{identityGateDescriptorB, `{"1":{"Tla":"ABC","RacingNumber":"1"},"7":{"Tla":"XYZ","RacingNumber":"7"}}`} {
		topic := []string{"SessionInfo", "DriverList"}[i]
		b.RequestedTopics = append(b.RequestedTopics, topic)
		b.PresentTopics = append(b.PresentTopics, topic)
		b.Updates = append(b.Updates, f1engine.Update{Topic: topic, Payload: []byte(p), Source: b.Source})
	}
	r, e := f1engine.ReduceBatch(f1engine.State{}, b)
	if e != nil {
		t.Fatal(e)
	}
	return r.State
}
func runtimeFeed(t *testing.T, s f1engine.State, sec int64, topic, p string) f1engine.State {
	at := fieldTime(sec)
	r, e := f1engine.ReduceBatch(s, f1engine.Batch{ObservationTime: at, Source: f1engine.Feed, Updates: []f1engine.Update{{Topic: topic, Payload: []byte(p), Timestamp: at, Source: f1engine.Feed}}})
	if e != nil {
		t.Fatal(e)
	}
	return r.State
}
func identityGateTestState(t *testing.T) f1engine.State {
	r, e := f1engine.ReduceBatch(f1engine.State{}, normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2022-01-01T00:00:02Z"))
	if e != nil {
		t.Fatal(e)
	}
	return r.State
}
func normalizeIdentityGateSessionInfoFeed(t *testing.T, p, ts string) f1engine.Batch {
	b, e := f1engine.NormalizeBatch(f1engine.WireBatch{Source: f1engine.Feed, Updates: []f1engine.WireUpdate{{Topic: "SessionInfo", Payload: []byte(p), Timestamp: ts, Source: f1engine.Feed}}}, time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func carRow(t time.Time, cars string) string {
	return fmt.Sprintf(`{"Utc":%q,"Cars":{%s}}`, t.Format(time.RFC3339Nano), cars)
}
func fieldCar(n int64, ch string) string { return fmt.Sprintf(`"%d":{"Channels":{%s}}`, n, ch) }
func compressedJSONPayload(t *testing.T, contents []byte) json.RawMessage {
	t.Helper()
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	if _, err := writer.Write(contents); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return json.RawMessage(strconv.Quote(base64.StdEncoding.EncodeToString(compressed.Bytes())))
}
