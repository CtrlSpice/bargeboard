// Package f1index discovers archived sessions in the public season index.
package f1index

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CtrlSpice/bargeboard/internal/f1engine"
	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

type Meeting struct {
	Key                          int64
	Name, OfficialName, Location string
	Country                      struct{ Name string }
	Circuit                      struct {
		Key       int64
		ShortName string
	}
	Sessions []Session
}

type Session struct {
	Key                                             int64
	Type, Name, StartDate, EndDate, GmtOffset, Path string
}

func Parse(raw []byte) ([]Meeting, error) {
	var index struct{ Meetings []Meeting }
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(raw) || json.Unmarshal(raw, &index) != nil || index.Meetings == nil || f1id.HasInvalidJSONScalars(raw) {
		return nil, errors.New("invalid F1 season index")
	}
	return index.Meetings, nil
}

var ErrAbsent = errors.New("season index unavailable")

func Fetch(ctx context.Context, endpoint string, year int) ([]Meeting, error) {
	if err := f1id.ValidateHTTPSOrLoopbackEndpoint("endpoint", endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%d/Index.json", endpoint, year), nil)
	if err != nil {
		return nil, errors.New("invalid index request")
	}
	resp, err := f1id.NewRestrictedHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, f1id.SanitizedHTTPError(ctx, "fetch season index", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, ErrAbsent
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("season index: HTTP %d", resp.StatusCode)
	}
	const limit = 4 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, f1id.SanitizedHTTPError(ctx, "read season index", err)
	}
	if len(b) > limit {
		return nil, f1id.ErrResponseTooLarge
	}
	return Parse(b)
}

type Selector struct {
	Year          int
	Meeting, Type string
}

func ParseSelector(s string) (Selector, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "latest" {
		return Selector{Meeting: "last", Type: "race"}, nil
	}
	p := strings.Split(s, "/")
	if len(p) == 3 && p[1] != "" && p[2] != "" {
		y, err := strconv.Atoi(p[0])
		if err == nil && y >= 1950 && y <= 9999 && strconv.Itoa(y) == p[0] {
			return Selector{y, p[1], p[2]}, nil
		}
	}
	return Selector{}, errors.New("expected latest or year/meeting/session")
}

type Match struct {
	Meeting string
	Session Session
}

var selectorSeparators = strings.NewReplacer("-", " ", "_", " ")

func normalizeSelectorPart(s string) string {
	return strings.Join(strings.Fields(selectorSeparators.Replace(strings.ToLower(s))), " ")
}

func selectSession(ms []Meeting, q Selector, now time.Time) (Match, bool) {
	meeting, sessionType := normalizeSelectorPart(q.Meeting), normalizeSelectorPart(q.Type)
	if meeting == "" || sessionType == "" && q.Type != "" {
		return Match{}, false
	}
	var result Match
	var latest time.Time
	for _, m := range ms {
		if meeting != "last" && !strings.Contains(normalizeSelectorPart(m.Name+" "+m.OfficialName+" "+m.Location+" "+m.Country.Name+" "+m.Circuit.ShortName), meeting) {
			continue
		}
		for _, s := range m.Sessions {
			if sessionType != "" && normalizeSelectorPart(s.Type) != sessionType && normalizeSelectorPart(s.Name) != sessionType {
				continue
			}
			end, ok := f1engine.LocalScheduleUTC(s.EndDate, s.GmtOffset)
			if !ok || end.After(now) || !end.After(latest) || s.Path == "" {
				continue
			}
			latest = end
			result = Match{m.Name, s}
		}
	}
	return result, !latest.IsZero()
}

func Resolve(ctx context.Context, endpoint string, q Selector, now time.Time) (Match, error) {
	years := []int{q.Year}
	if q.Year == 0 {
		year := now.UTC().Year()
		years = []int{year, year - 1}
	}
	for _, y := range years {
		ms, err := Fetch(ctx, endpoint, y)
		if errors.Is(err, ErrAbsent) && q.Year == 0 {
			continue
		}
		if err != nil {
			return Match{}, err
		}
		if m, ok := selectSession(ms, q, now); ok {
			return m, nil
		}
	}
	return Match{}, errors.New("no completed session matches selector")
}
