package f1staticreceiver

import (
	"context"
	"errors"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1engine"
)

type BatchConsumer func(ctx context.Context, batch FrozenBatch, processNow time.Time) error

type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}
type ProcessClock struct{}

func (ProcessClock) Now() time.Time { return time.Now() }
func (ProcessClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Replay delivers one plan in ordinal order. Time spent in the consumer
// counts towards the next deadline rather than stretching the replay.
func Replay(ctx context.Context, p FrozenReplayPlan, pace string, clock Clock, consume BatchConsumer) error {
	n, err := multiplier(pace)
	if err != nil {
		return err
	}
	if clock == nil || consume == nil || len(p.batches) == 0 {
		return errors.New("incomplete replay")
	}
	start := clock.Now()
	for _, b := range p.batches {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n != 0 {
			wait := start.Add(time.Duration(b.CoordNs / int64(n))).Sub(clock.Now())
			if wait > 0 {
				if err := clock.Sleep(ctx, wait); err != nil {
					return err
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := consume(ctx, b, clock.Now()); err != nil {
			return err
		}
	}
	return nil
}

func (b FrozenBatch) engineBatch() f1engine.Batch {
	batch := f1engine.Batch{ObservationTime: b.ObservationUTC, Source: f1engine.Feed}
	if b.Snapshot {
		batch.Source = f1engine.Snapshot
	}
	for _, rec := range b.records {
		batch.Updates = append(batch.Updates, f1engine.Update{Source: batch.Source, Topic: rec.Topic, Payload: []byte(rec.Payload), Timestamp: rec.PublicationUTC, InitialArchiveRadio: rec.initialArchiveRadio})
		if b.Snapshot {
			batch.RequestedTopics = append(batch.RequestedTopics, rec.Topic)
			batch.PresentTopics = append(batch.PresentTopics, rec.Topic)
		}
	}
	return batch
}
