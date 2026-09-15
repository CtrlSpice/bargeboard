package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHTTPRejectedNegotiationBodyIsUnread(t *testing.T) {
	for _, status := range []int{301, 401, 403, 404, 408, 429, 500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			body := &preflightTestBody{}
			client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}, nil
			})}
			got, cookies, err := negotiate(context.Background(), client, "https://synthetic.test/private-route", connectionCredentials{}, nil)
			if err == nil || got != (negotiation{}) || cookies != nil {
				t.Fatalf("negotiation = %#v, %v, %v", got, cookies, err)
			}
			if *body != (preflightTestBody{closes: 1}) {
				t.Fatalf("body operations = %+v; want zero reads and one close", body)
			}
		})
	}
}

func TestHTTPStageStatusMatrix(t *testing.T) {
	var success []int
	for status := 200; status <= 299; status++ {
		success = append(success, status)
	}
	// These rows encode the approved stage table, independently of the
	// classifier's stage/status branching. Every unlisted response is terminal.
	for _, contract := range []struct {
		stage, errorStage setupStage
		accepted, retry   []int
	}{
		{stage: 0},
		{stage: stagePreflight, errorStage: stagePreflight, accepted: success, retry: []int{408, 429, 500, 502, 503, 504}},
		{stage: stageNegotiate, errorStage: stageNegotiate, accepted: success, retry: []int{408, 429, 500, 502, 503, 504}},
		{stage: stageUpgrade, errorStage: stageUpgrade, accepted: []int{101}, retry: []int{404, 408, 429, 500, 502, 503, 504}},
		{stage: 255},
	} {
		expected := make(map[int]httpDisposition)
		for _, status := range contract.accepted {
			expected[status] = httpAccept
		}
		for _, status := range contract.retry {
			expected[status] = httpRetry
		}
		for status := 100; status <= 999; status++ {
			want, listed := expected[status]
			if !listed {
				want = httpStop
			}
			if got := classifySetupHTTP(contract.stage, status); got != want {
				t.Fatalf("stage %d HTTP %d = %d, want %d", contract.stage, status, got, want)
			}
			if want != httpAccept {
				now := time.Unix(100, 0)
				got := setupHTTPFailure(contract.stage, status, "60", now)
				wantError := setupHTTPError{stage: contract.errorStage, status: status}
				if want == httpRetry {
					wantError.retryAt = now.Add(time.Minute)
				}
				if *got != wantError || got.permanent() != (want == httpStop) || errors.Is(got, errInvalidLiveTimingData) {
					t.Fatalf("HTTP failure = %#v, want %#v", got, wantError)
				}
			}
		}
	}
	for _, status := range []int{-1, 0, 99, 1000, math.MaxInt} {
		if got := setupHTTPFailure(255, status, "60", time.Unix(1, 0)); *got != (setupHTTPError{}) || !got.permanent() {
			t.Fatalf("unbounded failure = %#v", got)
		}
	}
}

func TestHTTPPreflightCookieFirstMatrix(t *testing.T) {
	for status := 100; status <= 599; status++ {
		for _, cookie := range []bool{false, true} {
			cookies := []*http.Cookie{nil, {Name: "other", Value: "private"}, {Name: affinityCookieName}}
			want := connectionCredentials{}
			if cookie {
				cookies = append(cookies, &http.Cookie{Name: affinityCookieName, Value: "private"})
				want = connectionCredentials{token: "private", affinityCookie: &http.Cookie{Name: affinityCookieName, Value: "private"}}
			}
			got, err := credentialsFromPreflight("private", status, cookies)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("HTTP %d cookie=%v credentials mismatch", status, cookie)
			}
			if cookie {
				if err != nil {
					t.Fatal(err)
				}
			} else if status >= 200 && status < 300 {
				if !errors.Is(err, errInvalidLiveTimingData) || asSetupHTTPError(err) != nil {
					t.Fatalf("HTTP %d = %v", status, err)
				}
			} else if failure := asSetupHTTPError(err); failure == nil || *failure != (setupHTTPError{stage: stagePreflight, status: status}) {
				t.Fatalf("HTTP %d = %v", status, err)
			}
		}
	}
}

func TestHTTPRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 500000000, time.UTC)
	for _, test := range []struct {
		value string
		delay time.Duration
	}{
		{"0", 0}, {"000", 0}, {"1", time.Second}, {"00042", 42 * time.Second}, {" \t60\t ", time.Minute},
		{"9223372036", 9223372036 * time.Second},
		{"Mon, 14 Sep 2026 12:01:00 GMT", 59500 * time.Millisecond},
		{"Monday, 14-Sep-26 12:01:00 GMT", 59500 * time.Millisecond},
		{"Mon Sep 14 12:01:00 2026", 59500 * time.Millisecond},
		{" \tMon, 14 Sep 2026 12:01:00 GMT\t ", 59500 * time.Millisecond},
		{"Mon, 14 Sep 2026 12:00:00 GMT", 0}, {"Sun, 13 Sep 2026 12:01:00 GMT", 0},
		{"", 0}, {" \t", 0}, {"-1", 0}, {"+1", 0}, {"1.5", 0}, {"1e2", 0}, {"１", 0}, {"١", 0},
		{"1 2", 0}, {"1\n", 0}, {"\u00a01", 0}, {"1, 2", 0}, {"private-hint", 0},
		{"9223372037", 0}, {"18446744073709551616", 0}, {strings.Repeat("9", 100), 0},
		{"Fri, 31 Dec 9999 23:59:59 GMT", 0}, {"Mon, 99 Sep 2026 12:01:00 GMT", 0},
	} {
		t.Run(test.value, func(t *testing.T) {
			want := time.Time{}
			if test.delay > 0 {
				want = now.Add(test.delay)
			}
			if got := retryAfterDeadline(test.value, now); got != want {
				t.Fatalf("deadline = %v, want %v", got, want)
			}
		})
	}
	// Supplied process time must survive as a value, not just an equal wall time.
	monotonic := time.Now()
	if got := retryAfterDeadline("60", monotonic); got != monotonic.Add(time.Minute) {
		t.Fatalf("monotonic seconds = %v", got)
	}
	date := monotonic.Add(time.Minute).UTC().Truncate(time.Second)
	if got := retryAfterDeadline(date.Format(http.TimeFormat), monotonic); got != monotonic.Add(date.Sub(monotonic)) {
		t.Fatalf("monotonic date = %v", got)
	}
	// HTTP dates have second precision, but their distance from the supplied
	// observation may be exactly MaxInt64 nanoseconds, or overflow by just one.
	limit := date.Add(-time.Duration(math.MaxInt64))
	if got := retryAfterDeadline(date.Format(http.TimeFormat), limit); got != limit.Add(time.Duration(math.MaxInt64)) {
		t.Fatalf("exact duration limit = %v", got)
	}
	if got := retryAfterDeadline(date.Format(http.TimeFormat), limit.Add(-1)); !got.IsZero() {
		t.Fatalf("date overflow was accepted: %v", got)
	}
}

func TestHTTPErrorIsOpaqueAndExtractable(t *testing.T) {
	failure := setupHTTPFailure(stageUpgrade, 404, "private-hint", time.Unix(1, 0))
	for _, value := range []any{failure, *failure} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(format, value); got != "SignalR WebSocket upgrade returned HTTP 404" {
				t.Fatalf("formatted error = %q", got)
			}
		}
	}
	if got := asSetupHTTPError(fmt.Errorf("bounded wrapper: %w", failure)); got != failure || errors.Unwrap(got) != nil {
		t.Fatalf("error chain = %#v", got)
	}
	if asSetupHTTPError(errors.New("other")) != nil || asSetupHTTPError(nil) != nil {
		t.Fatal("non-HTTP error extracted")
	}
}

func TestHTTPRetryAfterRejectsMonotonicDeadlineOverflow(t *testing.T) {
	// Obtain a monotonic value, then place its reading above the sub-second
	// headroom left by the largest representable whole-second duration.
	now := time.Now().Add(time.Hour)
	if now == now.Round(0) {
		t.Fatal("the supported native clock did not supply a monotonic reading")
	}
	for _, value := range []string{
		"9223372036",
		now.Add(time.Duration(math.MaxInt64)).UTC().Format(http.TimeFormat),
	} {
		if got := retryAfterDeadline(value, now); !got.IsZero() {
			t.Fatalf("accepted a hint that loses the process clock: %v", got)
		}
	}
	if got := retryAfterDeadline("3600", now); got != now.Add(time.Hour) || got == got.Round(0) {
		t.Fatalf("ordinary retry deadline lost its monotonic reading: %v", got)
	}
}

func TestOperationalHTTPDeadlineFloor(t *testing.T) {
	at := time.Unix(100, 0)
	before := operationalState{started: at.Add(-time.Hour), outage: true, outageStarted: at.Add(-time.Minute), attempts: 3, outages: 1, updates: 2, ready: true}
	for _, notBefore := range []time.Time{{}, at.Add(-time.Second), at.Add(time.Second), at.Add(30 * time.Second), at.Add(time.Hour)} {
		want := before
		want.retryAt = at.Add(30 * time.Second)
		if notBefore.After(want.retryAt) {
			want.retryAt = notBefore
		}
		got, notice := reduceOperational(before, operationalInput{event: opSchedule, at: at, delay: 30 * time.Second, notBefore: notBefore})
		if got != want || notice != noticeProgress {
			t.Fatalf("schedule = %+v / %v, want %+v", got, notice, want)
		}
	}
	want := before
	want.stopped = true
	got, notice := reduceOperational(before, operationalInput{event: opSetupStopped, at: at, setupStage: stageNegotiate, httpStatus: 401})
	if got != want || notice != noticeSetupStopped {
		t.Fatalf("terminal = %+v / %v, want %+v", got, notice, want)
	}
	for _, event := range []operationalEvent{opConnected, opBatch, opSchedule, opAttempt, opSetupStopped} {
		if after, notice := reduceOperational(got, operationalInput{event: event, at: at, updates: 5}); after != got || notice != noticeNone {
			t.Fatal("stopped state changed")
		}
	}
}

func TestOperationalHTTPStopClearsConnectionAndSchedule(t *testing.T) {
	at := time.Unix(100, 0)
	for _, outage := range []bool{false, true} {
		before := operationalState{started: at.Add(-time.Hour), lastUpdate: at.Add(-time.Minute), retryAt: at.Add(time.Hour),
			connection: true, subscription: true, connectionData: true, ready: true, attempts: 5, updates: 3, recoveries: 2, outages: 2,
			consumerFailures: 1, closedOutageDuration: time.Minute}
		if outage {
			before.outage, before.outageStarted, before.outages = true, at.Add(-time.Second), 3
		}
		want := before
		want.connection, want.subscription, want.connectionData, want.retryAt = false, false, false, time.Time{}
		want.stopped, want.outage, want.outages = true, true, 3
		if !outage {
			want.outageStarted = at
		}
		got, notice := reduceOperational(before, operationalInput{event: opSetupStopped, at: at, setupStage: stageUpgrade, httpStatus: 403})
		if got != want || notice != noticeSetupStopped {
			t.Fatalf("stop=%+v / %v, want %+v", got, notice, want)
		}
	}
}
