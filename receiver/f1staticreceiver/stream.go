package f1staticreceiver

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type cursor struct {
	index          int
	compressed     bool
	reader         *bufio.Reader
	record         StreamRecord
	effective, seq int64
	invalid        error
}

func (c *cursor) line() ([]byte, error) {
	line, err := c.reader.ReadBytes('\n')
	if err != nil && !(err == io.EOF && len(line) > 0) {
		return nil, err
	}
	if err == nil {
		line = bytes.TrimSuffix(line[:len(line)-1], []byte{'\r'})
	}
	return line, nil
}

func (c *cursor) reset(f *os.File) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	c.reader.Reset(f)
	if prefix, _ := c.reader.Peek(3); bytes.Equal(prefix, []byte("\xef\xbb\xbf")) {
		_, _ = c.reader.Discard(3)
	}
	return nil
}

func (c *cursor) advance() error {
	line, err := c.line()
	if err != nil {
		return err
	}
	r, err := parseLine(line, c.compressed)
	if err != nil {
		// Keep a corrupt payload in merge order when its offset is valid.
		if len(line) >= 12 {
			if offset, ok := parseOffset(line[:12]); ok {
				c.effective = max(c.effective, offset)
				c.invalid = err
				return nil
			}
		}
		return err
	}
	c.record = r
	c.effective = max(c.effective, r.OffsetNs)
	c.seq++
	return nil
}

// Only the first unconsumed line of a topic can enter the heap, so line
// sequence breaks ties within a topic without an additional comparison.
type cursors []*cursor

func (h cursors) Len() int { return len(h) }
func (h cursors) Less(i, j int) bool {
	return h[i].effective < h[j].effective || h[i].effective == h[j].effective && h[i].index < h[j].index
}
func (h cursors) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *cursors) Push(v any)   { *h = append(*h, v.(*cursor)) }
func (h *cursors) Pop() any {
	a := *h
	i := len(a) - 1
	v := a[i]
	a[i] = nil
	*h = a[:i]
	return v
}

type streamPlan struct {
	FrozenReplayPlan
	snapshot FrozenBatch
	dir      string
	files    []*os.File
	heads    cursors
	minimum  int64
	t0       time.Time
}

func (p *streamPlan) close() {
	for _, f := range p.files {
		f.Close()
	}
	os.RemoveAll(p.dir)
}

// A regressing offset late in a file can set the bootstrap time. Scan raw
// prefixes first, inflating compressed payloads only during replay.
func (p *streamPlan) prepare(ctx context.Context, f *os.File, c *cursor) error {
	if err := c.reset(f); err != nil {
		return err
	}
	topic := topics[c.index]
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := c.line()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(line) < 13 {
			return errRecord
		}
		offset, ok := parseOffset(line[:12])
		if !ok {
			return errRecord
		}
		p.minimum = min(p.minimum, offset)
		if p.t0.IsZero() && (topic == "Heartbeat" || topic == "SessionData") {
			r, err := parseLine(line, false)
			if err != nil {
				return err
			}
			t, err := anchor(map[string][]StreamRecord{topic: {r}})
			if err == nil {
				p.t0 = t
			} else if !errors.Is(err, ErrMissingHistoricalOrigin) {
				return err
			}
		}
	}
	if err := c.reset(f); err != nil {
		return err
	}
	if err := c.advance(); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	if c.invalid != nil {
		return c.invalid
	}
	heap.Push(&p.heads, c)
	return nil
}

func (p *streamPlan) walk(ctx context.Context, sink batchSink) error {
	b := p.snapshot
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		last := len(p.heads) == 0
		if err := sink(b, last || p.heads[0].effective-p.minimum != b.CoordNs, last); err != nil {
			return err
		}
		if last {
			return nil
		}
		c := heap.Pop(&p.heads).(*cursor)
		topic := topics[c.index]
		if c.invalid != nil {
			return fmt.Errorf("%s: %w", topic, c.invalid)
		}
		pub := p.t0.Add(time.Duration(c.record.OffsetNs))
		if !validReplayTime(pub) {
			return errTimeline
		}
		b = FrozenBatch{b.Ordinal + 1, c.effective - p.minimum, p.t0.Add(time.Duration(c.effective)), false, []Record{{topic, c.record.Payload, pub, topic == "TeamRadio" && c.seq == 1}}}
		if err := c.advance(); err == nil {
			heap.Push(&p.heads, c)
		} else if err != io.EOF {
			return err
		}
	}
}
