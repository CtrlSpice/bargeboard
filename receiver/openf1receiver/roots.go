package openf1receiver

import (
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

// RootGate belongs to one Live Timing generation. The owner reduces Close
// in FIFO order with adapter completions, then exports the returned effects.
type RootGate struct {
	Generation uint64
	Closure
	closed bool
}

type RootBoundary uint8

const (
	Ends RootBoundary = iota + 1
	GraceExpiry
	Replacement
	Shutdown
)

type DriverRoot struct {
	Number               int64
	Exists, Participated bool
	Start, GreatestOwned time.Time
}

// ParticipationEvidence classifies a validated Live Timing effect, not raw
// topic data. Hydrated children and snapshot lap counts are not live effects.
type ParticipationEvidence struct {
	AfterCompetitiveStart, Live                                      bool
	LapCountTransition, LapCreated, SectorCreated, PitEntry, PitExit bool
}

func (p ParticipationEvidence) Participated() bool {
	return p.AfterCompetitiveStart && p.Live && (p.LapCountTransition || p.LapCreated || p.SectorCreated || p.PitEntry || p.PitExit)
}

type RootEffect struct {
	DriverNumber int64
	Start        time.Time
	RootOutcome
}

func (g RootGate) Close(generation uint64, boundary RootBoundary, source, observed time.Time, e f1id.SessionIdentity, s Snapshot, competitiveStart *time.Time, drivers []DriverRoot) (RootGate, []RootEffect, Diagnostic) {
	if g.closed || g.Generation != generation || !raceLike(e) {
		return g, nil, ""
	}
	var ends time.Time
	switch boundary {
	case Ends:
		if source.IsZero() {
			return g, nil, ""
		}
		ends = source
	case GraceExpiry:
		if g.GraceDeadline.IsZero() || observed.Before(g.GraceDeadline) {
			return g, nil, ""
		}
	case Replacement, Shutdown:
	default:
		return g, nil, ""
	}
	g.closed = true
	var out []RootEffect
	var diagnostic Diagnostic
	for _, d := range drivers {
		end := g.FallbackEnd(ends, d.Start, d.GreatestOwned, boundary == GraceExpiry)
		r := ResolveRootOutcome(e, d.Number, s.Driver(d.Number), d.Participated, competitiveStart, end)
		if r.Diagnostic != "" {
			diagnostic = r.Diagnostic
			r.Diagnostic = ""
		}
		if r.Suppress || !r.DNSPoint && !d.Exists {
			continue
		}
		start := d.Start
		if r.DNSPoint {
			start = r.End
		} else {
			r.Attributes.PutStr("f1.time.quality", "estimated")
		}
		out = append(out, RootEffect{d.Number, start, r})
	}
	return g, out, diagnostic
}
