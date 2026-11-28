package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	for _, mode := range []string{"default", "explicit", "absent"} {
		t.Run(mode, func(t *testing.T) { sessionsJSON(t, mode) })
	}
}

func sessionsJSON(t *testing.T, mode string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var flags []string
	var header string
	if mode != "absent" {
		file := filepath.Join(home, ".config", "bargeboard", "f1tv-token")
		if mode == "explicit" {
			file = filepath.Join(home, "explicit")
			flags = []string{"--token-file", file}
		}
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("invented-sessions"), 0600); err != nil {
			t.Fatal(err)
		}
		header = "Bearer invented-sessions"
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != header || (header == "" && r.Header.Values("Authorization") != nil) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/2023/Index.json" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"Meetings":[{"Name":"Example GP","Circuit":{"Key":98},"Sessions":[{"Key":26,"Type":"Race"}]}]}`)
	}))
	defer s.Close()
	var out bytes.Buffer
	if err := Run(t.Context(), otelcol.CollectorSettings{}, append([]string{"sessions", "2023", "--json", "--endpoint", s.URL}, flags...), &out); err != nil {
		t.Fatal(err)
	}
	var ms []f1index.Meeting
	if err := json.Unmarshal(out.Bytes(), &ms); err != nil || len(ms) != 1 || ms[0].Name != "Example GP" || ms[0].Circuit.Key != 98 || len(ms[0].Sessions) != 1 || ms[0].Sessions[0].Key != 26 {
		t.Fatal(out.String(), err)
	}
}

func TestArchiveCommandsRejectBadTokenFiles(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("command contacted the archive with an invalid token file")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer s.Close()
	for _, command := range []string{"sessions", "replay"} {
		for _, mode := range []string{"missing", "multiline", "empty-path", "invalid-default", "unreadable"} {
			t.Run(command+"/"+mode, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				file := filepath.Join(home, ".config", "bargeboard", "f1tv-token")
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if mode == "multiline" || mode == "invalid-default" {
					if err := os.WriteFile(file, []byte("private-example\nprivate-example"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "unreadable" {
					file = home
				}
				if mode == "empty-path" {
					file = ""
				}
				args := []string{command, "--endpoint", s.URL}
				if command == "replay" {
					args = append(args, "--once")
				}
				if mode != "invalid-default" {
					args = append(args, "--token-file", file)
				}
				err := Run(t.Context(), otelcol.CollectorSettings{}, args, io.Discard)
				if err == nil || !strings.Contains(err.Error(), "token file") || strings.Contains(err.Error(), "private-example") {
					t.Fatal("expected a sanitized token file error")
				}
			})
		}
	}
}
