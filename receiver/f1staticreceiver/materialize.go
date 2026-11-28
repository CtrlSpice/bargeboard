package f1staticreceiver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/circuitmap"
	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

var ErrMissingHistoricalOrigin = errors.New("archive has no historical UTC anchor")
var errTimeline = errors.New("archive time outside supported Unix nanosecond range")

func validReplayTime(t time.Time) bool {
	return t.Year() >= 1970 && t.Year() <= 2261 && t.UnixNano() > 0
}

var topics = [...]string{"SessionInfo", "Heartbeat", "DriverList", "Position", "SessionData", "TrackStatus", "ExtrapolatedClock", "LapCount", "WeatherData", "TimingAppData", "TimingStats", "TimingData", "PitLaneTimeCollection", "PitStopSeries", "PitStop", "RaceControlMessages", "TeamRadio"}

type Record struct {
	Topic, Payload string
	PublicationUTC time.Time
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
func localDate(raw []byte) time.Time {
	const layout = "2006-01-02T15:04:05"
	s := text(raw)
	t, err := time.Parse(layout, s)
	if err != nil || t.Format(layout) != s {
		return time.Time{}
	}
	return t
}

func bootstrap(info, roster []byte) (FrozenReplayPlan, error) {
	var p FrozenReplayPlan
	f := object(info)
	meeting := object(f["Meeting"])
	start := localDate(f["StartDate"])
	year := int64(start.Year())
	key, _ := f1id.ParsePositiveCanonicalInt64(meeting["Key"])
	typ, name, _ := f1id.ClassifySessionInfo(year, key, text(meeting["Name"]), text(f["Type"]), text(f["Name"]))
	p.Identity = f1id.SessionIdentity{Season: year, MeetingKey: key, SessionType: typ, SessionName: name}
	p.RouteKey, _ = f1id.ParsePositiveCanonicalInt64(f["Key"])
	p.CircuitKey, _ = f1id.ParsePositiveCanonicalInt64(object(meeting["Circuit"])["Key"])
	if !p.Identity.Valid() || p.RouteKey == 0 || f1id.HasInvalidJSONScalars(info) {
		return FrozenReplayPlan{}, errors.New("invalid SessionInfo")
	}
	end := localDate(f["EndDate"])
	zone := text(f["GmtOffset"])
	unsigned := strings.TrimPrefix(zone, "-")
	if off, ok := parseOffset([]byte(unsigned + ".000")); ok && strings.HasSuffix(unsigned, ":00") && off <= int64(14*time.Hour) && zone != "-00:00:00" && end.After(start) {
		if strings.HasPrefix(zone, "-") {
			off = -off
		}
		p.ScheduledStart = start.Add(-time.Duration(off))
		p.ScheduledEnd = end.Add(-time.Duration(off))
	}
	drivers := object(roster)
	if k, ok := drivers["_kf"]; ok && string(k) == "true" {
		delete(drivers, "_kf")
	}
	count := 0
	for k, v := range drivers {
		d := object(v)
		_, ok := f1id.ParsePositiveCanonicalInt64([]byte(k))
		if !ok {
			continue
		}
		count++
		if (d["RacingNumber"] != nil && text(d["RacingNumber"]) != k) || !f1id.IsValidAcronym(text(d["Tla"])) {
			return FrozenReplayPlan{}, errors.New("invalid DriverList entry")
		}
	}
	if count < 1 || count > 32 {
		return FrozenReplayPlan{}, errors.New("invalid DriverList")
	}
	return p, nil
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

func freeze(info, roster []byte, streams map[string][]StreamRecord) (FrozenReplayPlan, error) {
	p, err := bootstrap(info, roster)
	if err != nil {
		return FrozenReplayPlan{}, err
	}
	t0, err := anchor(streams)
	if err != nil {
		return FrozenReplayPlan{}, err
	}
	type item struct {
		effective int64
		r         Record
	}
	var items []item
	var minOffset int64 = 1<<63 - 1
	for _, topic := range topics {
		var effective int64
		for _, r := range streams[topic] {
			minOffset = min(minOffset, r.OffsetNs)
			effective = max(effective, r.OffsetNs)
			pub := t0.Add(time.Duration(r.OffsetNs))
			// The bootstrap and effective offsets also select from these checked offsets.
			if !validReplayTime(pub) {
				return FrozenReplayPlan{}, errTimeline
			}
			items = append(items, item{effective, Record{topic, r.Payload, pub}})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].effective < items[j].effective })
	origin := t0.Add(time.Duration(minOffset))
	p.batches = []FrozenBatch{{Snapshot: true, ObservationUTC: origin, records: []Record{{"SessionInfo", string(info), origin}, {"DriverList", string(roster), origin}}}}
	for i, it := range items {
		p.batches = append(p.batches, FrozenBatch{int64(i + 1), it.effective - minOffset, t0.Add(time.Duration(it.effective)), false, []Record{it.r}})
	}
	return p, nil
}

// Materialize performs all archive and geometry I/O before any batch is released.
func Materialize(ctx context.Context, c Config) (FrozenReplayPlan, error) {
	var empty FrozenReplayPlan
	if err := c.Validate(); err != nil {
		return empty, err
	}
	if !c.Enabled {
		return empty, errors.New("static replay disabled")
	}
	var token string
	if c.Auth.TokenFile != "" {
		b, err := os.ReadFile(c.Auth.TokenFile)
		if err != nil {
			return empty, errors.New("cannot read archive token")
		}
		token = strings.TrimSpace(string(b))
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return empty, errors.New("invalid archive token")
		}
	}
	client := f1id.NewRestrictedHTTPClient(60 * time.Second)
	fetch := func(file string, optional bool) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+"/"+c.SessionPath+"/"+file, nil)
		if err != nil {
			return nil, errors.New("invalid archive request")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, f1id.SanitizedHTTPError(ctx, "archive request", err)
		}
		defer resp.Body.Close()
		if optional && (resp.StatusCode == http.StatusNotFound || file == "TeamRadio.jsonStream" && resp.StatusCode == http.StatusForbidden) {
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("archive %s: HTTP %d", file, resp.StatusCode)
		}
		var limit int64 = 4 << 20
		if optional {
			limit = 64 << 20
		}
		if resp.ContentLength > limit {
			return nil, f1id.ErrResponseTooLarge
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, f1id.SanitizedHTTPError(ctx, "read archive", err)
		}
		if int64(len(b)) > limit {
			return nil, f1id.ErrResponseTooLarge
		}
		return b, nil
	}
	info, err := fetch("SessionInfo.json", false)
	if err != nil {
		return empty, err
	}
	roster, err := fetch("DriverList.json", false)
	if err != nil {
		return empty, err
	}
	streams := map[string][]StreamRecord{}
	for _, topic := range topics {
		if topic == "SessionInfo" || topic == "DriverList" {
			continue
		}
		file := topic + ".jsonStream"
		compressed := topic == "Position"
		if compressed {
			file = topic + ".z.jsonStream"
		}
		raw, err := fetch(file, true)
		if err != nil {
			return empty, err
		}
		if raw != nil {
			streams[topic], err = ParseStreamRecords(raw, compressed)
			if err != nil {
				return empty, fmt.Errorf("%s: %w", file, err)
			}
		}
	}
	bom := []byte("\xef\xbb\xbf")
	p, err := freeze(bytes.TrimPrefix(info, bom), bytes.TrimPrefix(roster, bom), streams)
	if err != nil {
		return empty, err
	}
	if c.CircuitEnrichment.Enabled && p.CircuitKey > 0 {
		m, err := circuitmap.NewClient(c.CircuitEnrichment.Endpoint)
		if err != nil {
			return empty, err
		}
		p.Circuit, _ = m.Load(ctx, p.CircuitKey, p.Identity.Season)
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return p, nil
}
