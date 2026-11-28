package f1staticreceiver

import (
	"sort"
	"time"
)

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
		for i, r := range streams[topic] {
			minOffset = min(minOffset, r.OffsetNs)
			effective = max(effective, r.OffsetNs)
			pub := t0.Add(time.Duration(r.OffsetNs))
			// The bootstrap and effective offsets also select from these checked offsets.
			if !validReplayTime(pub) {
				return FrozenReplayPlan{}, errTimeline
			}
			items = append(items, item{effective, Record{topic, r.Payload, pub, topic == "TeamRadio" && i == 0}})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].effective < items[j].effective })
	origin := t0.Add(time.Duration(minOffset))
	p.batches = []FrozenBatch{{Snapshot: true, ObservationUTC: origin, records: []Record{{Topic: "SessionInfo", Payload: string(info), PublicationUTC: origin}, {Topic: "DriverList", Payload: string(roster), PublicationUTC: origin}}}}
	for i, it := range items {
		p.batches = append(p.batches, FrozenBatch{int64(i + 1), it.effective - minOffset, t0.Add(time.Duration(it.effective)), false, []Record{it.r}})
	}
	return p, nil
}
