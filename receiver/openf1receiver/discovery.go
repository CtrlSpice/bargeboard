package openf1receiver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

func (h *HTTPAdapter) discover(ctx context.Context, c Config, now time.Time) (Config, error) {
	q := url.Values{}
	year := max(c.Year, c.Season)
	if year != 0 {
		q.Set("year", strconv.FormatInt(year, 10))
	}
	if c.MeetingKey != 0 {
		q.Set("meeting_key", strconv.FormatInt(c.MeetingKey, 10))
	}
	if c.CountryName != "" {
		q.Set("country_name", c.CountryName)
	}
	q.Set("session_type", "Race")
	if c.SessionName != "" {
		name := "Race"
		if c.SessionName == "sprint" {
			name = "Sprint"
		}
		q.Set("session_name", name)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.endpoint+"/sessions?"+q.Encode(), nil)
	if err != nil {
		return c, errors.New("invalid OpenF1 request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := h.transport.RoundTrip(req)
	if err != nil {
		return c, f1id.SanitizedHTTPError(ctx, "OpenF1 discovery", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return c, errors.New("OpenF1 session discovery failed")
	}
	body, err := f1id.ReadBoundedJSONBodyContext(ctx, resp, maxResponseBytes)
	if err != nil {
		return c, err
	}
	return chooseSession(c, body, now)
}
func chooseSession(c Config, body []byte, now time.Time) (Config, error) {
	var result Config
	var latest time.Time
	err := f1id.VisitArray(body, func(_ int, raw json.RawMessage) error {
		var f fields
		if json.Unmarshal(raw, &f) != nil {
			return errInvalid
		}
		s := c
		s.SessionKey = f.number("session_key")
		s.MeetingKey = f.number("meeting_key")
		s.Season = f.number("year")
		s.SessionName = f1id.SessionName(strings.ToLower(f.text("session_name")))
		end, ok := f1id.ParseStrictRFC3339(f.text("date_end"))
		year := max(c.Year, c.Season)
		if !ok || end.After(now) || !end.After(latest) || !raceLike(s.identity()) ||
			c.SessionName != "" && s.SessionName != c.SessionName ||
			c.MeetingKey != 0 && s.MeetingKey != c.MeetingKey || year != 0 && year != s.Season ||
			c.CountryName != "" && !strings.EqualFold(c.CountryName, f.text("country_name")) {
			return nil
		}
		single := append(append([]byte{'['}, raw...), ']')
		if _, d := parse(single, sessions, s.identity(), s.SessionKey, nil); d == "" && s.SessionKey > 0 {
			latest = end
			result = s
		}
		return nil
	})
	if err != nil || latest.IsZero() {
		return c, errors.New("no matching completed OpenF1 Race or Sprint")
	}
	return result, nil
}
