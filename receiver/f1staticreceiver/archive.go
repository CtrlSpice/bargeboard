// Package f1staticreceiver reads original-time Formula 1 static archives.
package f1staticreceiver

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

var errRecord = errors.New("invalid static archive record")

// StreamRecord retains the original publication offset even when it regresses.
// Payload contains immutable normalized JSON, including for compressed streams.
type StreamRecord struct {
	OffsetNs int64
	Payload  string
}

func parseOffset(raw []byte) (int64, bool) {
	if len(raw) != 12 || raw[2] != ':' || raw[5] != ':' || raw[8] != '.' {
		return 0, false
	}
	var d [12]int64
	for i, b := range raw {
		if i == 2 || i == 5 || i == 8 {
			continue
		}
		if b < '0' || b > '9' {
			return 0, false
		}
		d[i] = int64(b - '0')
	}
	m, s := 10*d[3]+d[4], 10*d[6]+d[7]
	if m >= 60 || s >= 60 {
		return 0, false
	}
	ms := (((10*d[0]+d[1])*60+m)*60+s)*1000 + 100*d[9] + 10*d[10] + d[11]
	return ms * int64(time.Millisecond), true
}

func inflate(raw []byte) ([]byte, error) {
	text, err := f1id.DecodeLosslessString(raw)
	if err != nil || len(text) > 64<<10 {
		return nil, errRecord
	}
	data, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || base64.StdEncoding.EncodeToString(data) != text {
		return nil, errRecord
	}
	input := bytes.NewReader(data)
	z := flate.NewReader(input)
	defer z.Close()
	out, err := io.ReadAll(io.LimitReader(z, (1<<20)+1))
	if err != nil || len(out) > 1<<20 || input.Len() != 0 {
		return nil, errRecord
	}
	return out, nil
}

// ParseStreamRecords validates the entire file before returning any records.
func ParseStreamRecords(data []byte, compressed bool) ([]StreamRecord, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var records []StreamRecord
	for len(data) > 0 {
		line, rest, newline := bytes.Cut(data, []byte{'\n'})
		if newline {
			line = bytes.TrimSuffix(line, []byte{'\r'})
		}
		if len(line) < 13 || bytes.ContainsRune(line, '\r') {
			return nil, errRecord
		}
		offset, ok := parseOffset(line[:12])
		if !ok {
			return nil, errRecord
		}
		raw := line[12:]
		if compressed {
			var err error
			raw, err = inflate(raw)
			if err != nil {
				return nil, err
			}
		}
		if !utf8.Valid(raw) || !json.Valid(raw) {
			return nil, errRecord
		}
		records = append(records, StreamRecord{offset, string(raw)})
		data = rest
	}
	return records, nil
}
