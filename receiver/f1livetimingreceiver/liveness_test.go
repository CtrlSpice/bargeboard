package f1livetimingreceiver

import (
	"testing"
	"time"
)

func TestServerWaitBudget(t *testing.T) {
	for _, test := range []struct {
		name       string
		initial    serverWaitBudget
		elapsed    time.Duration
		accept     bool
		completion bool
		want       serverWaitBudget
		remaining  time.Duration
		expired    error
	}{
		{name: "initial receive only", initial: newServerWaitBudget(false), want: serverWaitBudget{receive: 30 * time.Second}, remaining: 30 * time.Second},
		{name: "initial subscription", initial: newServerWaitBudget(true), want: serverWaitBudget{30 * time.Second, 30 * time.Second, true}, remaining: 30 * time.Second},
		{name: "remaining", initial: newServerWaitBudget(true), elapsed: 12 * time.Second, want: serverWaitBudget{18 * time.Second, 18 * time.Second, true}, remaining: 18 * time.Second},
		{name: "before expiry", initial: newServerWaitBudget(true), elapsed: 30*time.Second - 1, want: serverWaitBudget{1, 1, true}, remaining: 1},
		{name: "receive boundary", initial: newServerWaitBudget(false), elapsed: 30 * time.Second, want: serverWaitBudget{}, expired: errSignalRReceiveTimeout},
		{name: "subscription boundary", initial: newServerWaitBudget(true), elapsed: 30 * time.Second, want: serverWaitBudget{pending: true}, expired: errSignalRSubscriptionTimeout},
		{name: "after expiry saturates", initial: newServerWaitBudget(true), elapsed: 30*time.Second + 1, want: serverWaitBudget{pending: true}, expired: errSignalRSubscriptionTimeout},
		{name: "ping or ignored record", initial: newServerWaitBudget(true), elapsed: 12 * time.Second, accept: true, want: serverWaitBudget{30 * time.Second, 18 * time.Second, true}, remaining: 18 * time.Second},
		{name: "partial or empty read", initial: serverWaitBudget{20 * time.Second, 10 * time.Second, true}, elapsed: 9 * time.Second, want: serverWaitBudget{11 * time.Second, time.Second, true}, remaining: time.Second},
		{name: "completion disables subscription", initial: newServerWaitBudget(true), elapsed: 29 * time.Second, accept: true, completion: true, want: serverWaitBudget{receive: 30 * time.Second}, remaining: 30 * time.Second},
		{name: "completed stays disabled", initial: serverWaitBudget{receive: 30 * time.Second}, elapsed: 29 * time.Second, want: serverWaitBudget{receive: time.Second}, remaining: time.Second},
		{name: "local work supplies zero wait", initial: serverWaitBudget{20 * time.Second, 10 * time.Second, true}, want: serverWaitBudget{20 * time.Second, 10 * time.Second, true}, remaining: 10 * time.Second},
		{name: "subscription expires despite receive resets", initial: serverWaitBudget{30 * time.Second, time.Second, true}, elapsed: time.Second, want: serverWaitBudget{29 * time.Second, 0, true}, expired: errSignalRSubscriptionTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := test.initial.wait(test.elapsed)
			if test.accept {
				got = got.accepted(test.completion)
			}
			if got != test.want || got.remaining() != test.remaining || got.expired() != test.expired {
				t.Errorf("state = %+v, remaining = %s, expired = %v; want %+v, %s, %v", got, got.remaining(), got.expired(), test.want, test.remaining, test.expired)
			}
		})
	}
}

func TestHubPingDelay(t *testing.T) {
	last := time.Unix(100, 0)
	for _, test := range []struct{ elapsed, want time.Duration }{
		{0, 15 * time.Second},
		{14 * time.Second, time.Second},
		{15*time.Second - 1, 1},
		{15 * time.Second, 0},
		{15*time.Second + 1, 0},
		{30 * time.Second, 0},
	} {
		if got := hubPingDelay(last, last.Add(test.elapsed)); got != test.want {
			t.Errorf("delay after %s = %s, want %s", test.elapsed, got, test.want)
		}
	}
}
