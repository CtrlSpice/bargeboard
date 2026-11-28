package f1staticreceiver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/archiveauth"
	"github.com/CtrlSpice/bargeboard/internal/circuitmap"
	"github.com/CtrlSpice/bargeboard/internal/f1engine"
	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"github.com/CtrlSpice/bargeboard/internal/f1index"
)

var ErrMissingHistoricalOrigin = errors.New("archive has no historical UTC anchor")
var errTimeline = errors.New("archive time outside supported Unix nanosecond range")

func validReplayTime(t time.Time) bool {
	return t.Year() >= 1970 && t.Year() <= 2261 && t.UnixNano() > 0
}

var topics = [...]string{"SessionInfo", "Heartbeat", "DriverList", "Position", "CarData", "SessionData", "TrackStatus", "ExtrapolatedClock", "LapCount", "WeatherData", "TimingAppData", "TimingStats", "TimingData", "PitLaneTimeCollection", "PitStopSeries", "PitStop", "RaceControlMessages", "TeamRadio"}

type Record struct {
	Topic, Payload      string
	PublicationUTC      time.Time
	initialArchiveRadio bool
}
type FrozenBatch struct {
	Ordinal, CoordNs int64
	ObservationUTC   time.Time
	Snapshot         bool
	records          []Record
}

func (b FrozenBatch) Records() []Record { return slices.Clone(b.records) }

type FrozenReplayPlan struct {
	Identity                     f1id.SessionIdentity
	RouteKey, CircuitKey         int64
	ScheduledStart, ScheduledEnd time.Time
	Circuit                      circuitmap.Map
	batches                      []FrozenBatch
}

func (p FrozenReplayPlan) Batches() []FrozenBatch { return slices.Clone(p.batches) }

type fields map[string]json.RawMessage

func object(raw []byte) fields {
	f := fields{}
	err := f1id.VisitObject(raw, func(k string, _, v json.RawMessage) error {
		if _, dup := f[k]; dup {
			return errRecord
		}
		f[k] = v
		return nil
	})
	if err != nil {
		return nil
	}
	return f
}
func text(raw []byte) string { s, _ := f1id.DecodeLosslessString(raw); return s }
func bootstrap(info, roster []byte) (FrozenReplayPlan, error) {
	// Archive materialization rejects duplicate top-level members and invalid
	// SessionInfo scalars before any frozen input can reach a consumer.
	if object(info) == nil || f1id.HasInvalidJSONScalars(info) {
		return FrozenReplayPlan{}, errors.New("invalid SessionInfo")
	}
	if object(roster) == nil {
		return FrozenReplayPlan{}, errors.New("invalid DriverList")
	}
	m, err := f1engine.InspectBootstrap(info, roster)
	if err != nil {
		return FrozenReplayPlan{}, err
	}
	return FrozenReplayPlan{Identity: m.Identity, RouteKey: m.RouteKey, CircuitKey: m.CircuitKey, ScheduledStart: m.ScheduledStart, ScheduledEnd: m.ScheduledEnd}, nil
}

// indexed returns series elements in numerical index order.
func indexed(raw []byte) []json.RawMessage {
	var a []json.RawMessage
	if json.Unmarshal(raw, &a) == nil {
		return a
	}
	m := object(raw)
	byIndex := map[int64]json.RawMessage{}
	var indexes []int64
	for k, v := range m {
		if i, ok := f1id.ParseNonNegativeCanonicalInt64([]byte(k)); ok {
			byIndex[i] = v
			indexes = append(indexes, i)
		}
	}
	slices.Sort(indexes)
	for _, i := range indexes {
		a = append(a, byIndex[i])
	}
	return a
}

func anchor(streams map[string][]StreamRecord) (time.Time, error) {
	for _, topic := range []string{"Heartbeat", "SessionData"} {
		for _, r := range streams[topic] {
			vals := []json.RawMessage{json.RawMessage(r.Payload)}
			if topic == "SessionData" {
				vals = indexed(object(vals[0])["StatusSeries"])
			}
			for _, v := range vals {
				if t, ok := f1id.ParseStrictRFC3339(text(object(v)["Utc"])); ok {
					origin := t.Add(-time.Duration(r.OffsetNs))
					if !validReplayTime(t) || !validReplayTime(origin) {
						return time.Time{}, errTimeline
					}
					return origin, nil
				}
			}
		}
	}
	return time.Time{}, ErrMissingHistoricalOrigin
}

// Materialize performs all archive and geometry I/O before any batch is released.
func Materialize(ctx context.Context, c Config) (FrozenReplayPlan, error) {
	p, err := openPlan(ctx, c)
	if err != nil {
		return FrozenReplayPlan{}, err
	}
	defer p.close()
	err = p.walk(ctx, func(b FrozenBatch, _, _ bool) error { p.batches = append(p.batches, b); return nil })
	if err != nil {
		return FrozenReplayPlan{}, err
	}
	return p.FrozenReplayPlan, nil
}

func openPlan(ctx context.Context, c Config) (*streamPlan, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, errors.New("static replay disabled")
	}
	var token string
	if c.Auth.TokenFile != "" {
		var err error
		token, err = archiveauth.Read(c.Auth.TokenFile)
		if err != nil {
			return nil, err
		}
	}
	if c.SessionPath == "" {
		q, _ := f1index.ParseSelector(c.Session)
		match, err := f1index.Resolve(ctx, c.Endpoint, q, time.Now(), token)
		if err != nil {
			return nil, err
		}
		c.SessionPath = strings.TrimSuffix(match.Session.Path, "/")
		if c.SessionPath == "" {
			return nil, errors.New("empty archive path")
		}
		if err := c.Validate(); err != nil {
			return nil, err
		}
	}
	client := f1id.NewRestrictedHTTPClient(60 * time.Second)
	fetch := func(file string, optional bool, dst io.Writer) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+"/"+c.SessionPath+"/"+file, nil)
		if err != nil {
			return false, errors.New("invalid archive request")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, f1id.SanitizedHTTPError(ctx, "archive request", err)
		}
		defer resp.Body.Close()
		if optional && (resp.StatusCode == http.StatusNotFound || file == "TeamRadio.jsonStream" && resp.StatusCode == http.StatusForbidden) {
			return false, nil
		}
		if resp.StatusCode != http.StatusOK {
			return false, archiveauth.StatusError("archive "+file, resp.StatusCode)
		}
		var limit int64 = 4 << 20
		if optional {
			limit = 64 << 20
		}
		if resp.ContentLength > limit {
			return false, f1id.ErrResponseTooLarge
		}
		n, err := io.Copy(dst, io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return false, f1id.SanitizedHTTPError(ctx, "read archive", err)
		}
		if n > limit {
			return false, f1id.ErrResponseTooLarge
		}
		return true, nil
	}
	var info, roster bytes.Buffer
	if _, err := fetch("SessionInfo.json", false, &info); err != nil {
		return nil, err
	}
	if _, err := fetch("DriverList.json", false, &roster); err != nil {
		return nil, err
	}
	bom := []byte("\xef\xbb\xbf")
	ib, rb := bytes.TrimPrefix(info.Bytes(), bom), bytes.TrimPrefix(roster.Bytes(), bom)
	meta, err := bootstrap(ib, rb)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bargeboard-replay-")
	if err != nil {
		return nil, err
	}
	p := &streamPlan{FrozenReplayPlan: meta, dir: dir, minimum: 1<<63 - 1}
	ok := false
	defer func() {
		if !ok {
			p.close()
		}
	}()
	for index, topic := range topics {
		if topic == "SessionInfo" || topic == "DriverList" {
			continue
		}
		compressed := topic == "CarData" || topic == "Position"
		file := topic + ".jsonStream"
		if compressed {
			file = topic + ".z.jsonStream"
		}
		f, err := os.Create(filepath.Join(dir, file))
		if err != nil {
			return nil, err
		}
		p.files = append(p.files, f)
		present, err := fetch(file, true, f)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		cur := &cursor{index: index, compressed: compressed, reader: bufio.NewReader(f)}
		if err = p.prepare(ctx, f, cur); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	}
	if p.t0.IsZero() {
		return nil, ErrMissingHistoricalOrigin
	}
	origin := p.t0.Add(time.Duration(p.minimum))
	if !validReplayTime(origin) {
		return nil, errTimeline
	}
	p.snapshot = FrozenBatch{Snapshot: true, ObservationUTC: origin, records: []Record{{Topic: "SessionInfo", Payload: string(ib), PublicationUTC: origin}, {Topic: "DriverList", Payload: string(rb), PublicationUTC: origin}}}
	if c.CircuitEnrichment.Enabled && p.CircuitKey > 0 {
		m, err := circuitmap.NewClient(c.CircuitEnrichment.Endpoint)
		if err != nil {
			return nil, err
		}
		p.Circuit, _ = m.Load(ctx, p.CircuitKey, p.Identity.Season)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok = true
	return p, nil
}
