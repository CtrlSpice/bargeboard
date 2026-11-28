package archiveauth

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, raw := range []string{"", " \r\n", "invented\nsecond", "invented\rsecond"} {
		got, err := parse([]byte(raw))
		if err == nil || got != "" || strings.Contains(err.Error(), "invented") {
			t.Fatal("expected a sanitized validation error")
		}
	}
	if got, err := parse([]byte("  invented\t\r\n")); err != nil || got != "invented" {
		t.Fatal("expected trimmed token")
	}
}
