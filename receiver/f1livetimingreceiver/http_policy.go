package f1livetimingreceiver

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

type setupStage uint8

const (
	stagePreflight setupStage = iota + 1
	stageNegotiate
	stageUpgrade
)

func (s setupStage) String() string {
	switch s {
	case stagePreflight:
		return "negotiation preflight"
	case stageNegotiate:
		return "SignalR negotiation"
	case stageUpgrade:
		return "SignalR WebSocket upgrade"
	default:
		return "unknown setup stage"
	}
}

type httpDisposition uint8

const (
	httpStop httpDisposition = iota
	httpAccept
	httpRetry
)

// Preflight's cookie-first decision precedes this status-only table.
func classifySetupHTTP(stage setupStage, status int) httpDisposition {
	switch stage {
	case stagePreflight, stageNegotiate:
		if status >= 200 && status < 300 {
			return httpAccept
		}
	case stageUpgrade:
		if status == 101 {
			return httpAccept
		}
		if status == 404 {
			return httpRetry // A new connection token requires fresh negotiation.
		}
	default:
		return httpStop
	}
	switch status {
	case 408, 429, 500, 502, 503, 504:
		return httpRetry
	default:
		return httpStop
	}
}

// Only bounded metadata and an observed process deadline survive HTTP setup.
// In particular, this error never wraps a URL error or retains response headers.
type setupHTTPError struct {
	stage   setupStage
	status  int
	retryAt time.Time
}

func (e setupHTTPError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.stage, e.status)
}

func (e setupHTTPError) GoString() string { return e.Error() }

func (e setupHTTPError) permanent() bool {
	return classifySetupHTTP(e.stage, e.status) != httpRetry
}

func setupHTTPFailure(stage setupStage, status int, retryAfter string, now time.Time) *setupHTTPError {
	if stage < stagePreflight || stage > stageUpgrade {
		stage = 0
	}
	if status < 100 || status > 999 {
		status = 0
	}
	e := &setupHTTPError{stage: stage, status: status}
	if !e.permanent() {
		e.retryAt = retryAfterDeadline(retryAfter, now)
	}
	return e
}

func asSetupHTTPError(err error) *setupHTTPError {
	var result *setupHTTPError
	if errors.As(err, &result) {
		return result
	}
	return nil
}

// HTTP dates use wall time only to calculate a duration at observation. Add
// preserves now's monotonic reading for every subsequent schedule and countdown.
// A zero deadline means no extra delay (including malformed/overflowing hints).
func retryAfterDeadline(value string, now time.Time) time.Time {
	value = strings.Trim(value, " \t")
	if value == "" {
		return time.Time{}
	}
	seconds := int64(0)
	digits := true
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			digits = false
			break
		}
		digit := int64(value[i] - '0')
		if seconds > (math.MaxInt64/int64(time.Second)-digit)/10 {
			return time.Time{}
		}
		seconds = seconds*10 + digit
	}
	if digits {
		if seconds == 0 {
			return time.Time{}
		}
		return processRetryDeadline(now, time.Duration(seconds)*time.Second)
	}
	date, err := http.ParseTime(value)
	if err != nil || !date.After(now) {
		return time.Time{}
	}
	delay := date.Sub(now)
	// Time.Sub saturates on overflow; do not turn an unrepresentable date into
	// a different accepted deadline. The round trip also accepts exact MaxInt64.
	if !now.Add(delay).Equal(date) {
		return time.Time{}
	}
	return processRetryDeadline(now, delay)
}

func processRetryDeadline(now time.Time, delay time.Duration) time.Time {
	deadline := now.Add(delay)
	// Time.Add drops the monotonic reading if its internal value overflows,
	// even when delay itself fits time.Duration. Do not fall back to wall time.
	if now != now.Round(0) && deadline == deadline.Round(0) {
		return time.Time{}
	}
	return deadline
}
