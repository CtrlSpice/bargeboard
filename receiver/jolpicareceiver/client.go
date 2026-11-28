package jolpicareceiver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

func retryDelay(raw string, now time.Time) time.Duration {
	const floor = 30 * time.Second
	if raw != "" && strings.Trim(raw, "0123456789") == "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || n > math.MaxInt64/uint64(time.Second) {
			return math.MaxInt64
		}
		return max(floor, time.Duration(n)*time.Second)
	}
	if at, err := http.ParseTime(raw); err == nil {
		return max(floor, at.Sub(now))
	}
	return floor
}

type rateLimit struct{ delay time.Duration }

func (rateLimit) Error() string { return "Jolpica request rate limited" }
func (r *roundReceiver) fetch(ctx context.Context, t table) ([]row, error) {
	c := r.cfg
	season, round := strconv.FormatInt(c.Season, 10), strconv.FormatInt(c.Round, 10)
	if c.Season == 0 {
		season = "current"
	}
	if c.Round == 0 {
		round = "last"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s/%s/%s.json?limit=100", c.Endpoint, season, round, t), nil)
	if err != nil {
		return nil, errors.New("invalid Jolpica request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "bargeboard/1.0")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, f1id.SanitizedHTTPError(ctx, "Jolpica request", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		return nil, rateLimit{retryDelay(resp.Header.Get("Retry-After"), r.now())}
	}
	if resp.StatusCode != 200 {
		return nil, errors.New("unexpected Jolpica HTTP status")
	}
	body, err := f1id.ReadBoundedJSONBodyContext(ctx, resp, 512<<10)
	if err != nil {
		return nil, f1id.SanitizedHTTPError(ctx, "Jolpica response body", err)
	}
	if c.Round == 0 {
		root, err := readObject(body)
		if err != nil {
			return nil, err
		}
		m, err := readObject(root["MRData"])
		if err != nil {
			return nil, err
		}
		tab, err := readObject(m["RaceTable"])
		if err != nil {
			return nil, err
		}
		var ok bool
		year, ok := integer(tab.text("season"), 1950, 9999)
		if !ok || c.Season != 0 && c.Season != year {
			return nil, errData
		}
		c.Season = year
		c.Round, ok = integer(tab.text("round"), 1, 99)
		if !ok {
			return nil, errData
		}
	}
	rows, err := parse(c, t, body)
	if err == nil {
		r.cfg.Season, r.cfg.Round = c.Season, c.Round
	}
	return rows, err
}
