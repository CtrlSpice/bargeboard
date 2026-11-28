package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/cli"
	"go.opentelemetry.io/collector/otelcol"
)

func TestReplayOncePersistsLinkedHistograms(t *testing.T) {
	files := map[string]string{
		"/2025/Index.json":                           `{"Meetings":[{"Name":"Invented Grand Prix","Sessions":[{"Type":"Race","Name":"Race","EndDate":"2025-03-01T10:30:00","GmtOffset":"00:00:00","Path":"2025/invented/race/"}]}]}`,
		"/2025/invented/race/SessionInfo.json":       `{"Key":23,"Meeting":{"Key":15,"Name":"Invented Grand Prix"},"Type":"Race","Name":"Race","StartDate":"2025-03-01T10:00:00","EndDate":"2025-03-01T10:30:00","GmtOffset":"00:00:00"}`,
		"/2025/invented/race/DriverList.json":        `{"13":{"RacingNumber":"13","Tla":"SYN"}}`,
		"/2025/invented/race/Heartbeat.jsonStream":   `00:00:00.000{"Utc":"2025-03-01T10:00:00Z"}`,
		"/2025/invented/race/SessionData.jsonStream": "00:00:00.000{\"StatusSeries\":[{\"Utc\":\"2025-03-01T10:00:00Z\",\"SessionStatus\":\"Started\"}]}\n00:00:20.000{\"StatusSeries\":{\"1\":{\"Utc\":\"2025-03-01T10:00:20Z\",\"SessionStatus\":\"Finished\"}}}",
		"/2025/invented/race/CarData.z.jsonStream":   deflateBase64JSONLine(t, "00:00:03.000", `{"Entries":[{"Utc":"2025-03-01T10:00:03Z","Cars":{"13":{"Channels":{"0":10000,"2":200}}}}]}`),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := files[r.URL.Path]; ok {
			io.WriteString(w, data)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "replay.duckdb")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	err := cli.Run(ctx, otelcol.CollectorSettings{Factories: components, SkipSettingGRPCLogger: true}, []string{"replay", "2025/last/race", "--endpoint", srv.URL, "--once", "--no-circuit", "--db", path, "--listen", "127.0.0.1:0"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]string
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil || summary["meeting"] != "Invented Grand Prix" || summary["session"] != "Race" || summary["db_path"] != path || len(summary["trace_id_sample"]) != 32 {
		t.Fatal(out.String(), err)
	}
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	err = db.QueryRowContext(ctx, `select count(distinct m.name) from metric_records m,
	unnest(m.datapoints) p(point), unnest(point.exemplars) e(exemplar), spans s
	where m.name in ('f1.field.speed','f1.field.engine_speed') and point.scale is not null
	and m.aggregation_temporality=1 and exemplar.trace_id=s.trace_id and exemplar.span_id=s.span_id
	and replace(s.trace_id::varchar,'-','')=?`, summary["trace_id_sample"]).Scan(&count)
	if err != nil || count != 2 {
		t.Fatalf("persisted histograms linked to sample driver: %d, %v", count, err)
	}
}
