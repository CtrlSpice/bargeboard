package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/CtrlSpice/bargeboard/internal/f1index"
	skill "github.com/CtrlSpice/bargeboard/skills/bargeboard"
	"go.opentelemetry.io/collector/otelcol"
)

func TestSkills(t *testing.T) {
	var out bytes.Buffer
	run := func(args ...string) {
		t.Helper()
		if err := Run(t.Context(), otelcol.CollectorSettings{}, args, &out); err != nil {
			t.Fatal(err)
		}
	}
	run("skills")
	if out.String() != skill.Skill {
		t.Fatal("printed skill differs from embedded guide")
	}
	dir := t.TempDir()
	out.Reset()
	run("skills", "--install", dir)
	b, err := os.ReadFile(filepath.Join(dir, "bargeboard", "SKILL.md"))
	if err != nil || string(b) != skill.Skill || out.Len() != 0 {
		t.Fatal("incorrect installed skill", err)
	}
}
func TestSessionsJSON(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2023/Index.json" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"Meetings":[{"Name":"Example GP","Circuit":{"Key":98},"Sessions":[{"Key":26,"Type":"Race"}]}]}`)
	}))
	defer s.Close()
	var out bytes.Buffer
	if err := Run(t.Context(), otelcol.CollectorSettings{}, []string{"sessions", "2023", "--json", "--endpoint", s.URL}, &out); err != nil {
		t.Fatal(err)
	}
	var ms []f1index.Meeting
	if err := json.Unmarshal(out.Bytes(), &ms); err != nil || len(ms) != 1 || ms[0].Name != "Example GP" || ms[0].Circuit.Key != 98 || len(ms[0].Sessions) != 1 || ms[0].Sessions[0].Key != 26 {
		t.Fatal(out.String(), err)
	}
}
