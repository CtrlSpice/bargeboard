package f1livetimingreceiver

import (
	"reflect"
	"testing"
	"time"
)

func TestOperationalLifecycle(t *testing.T) {
	at := time.Unix(100, 0)
	want := operationalState{}
	state := operationalState{}
	step := func(input operationalInput, notice operationalNotice) {
		t.Helper()
		input.at = at
		got, effect := reduceOperational(state, input)
		if got != want || effect != notice {
			t.Fatalf("event %d: state = %+v, notice = %d; want %+v, %d", input.event, got, effect, want, notice)
		}
		state = got
	}
	want.started = at
	step(operationalInput{event: opStart}, noticeStarting)
	want.connection = true
	step(operationalInput{event: opConnected}, noticeNone)
	step(operationalInput{event: opTick}, noticeWaiting)
	want.subscription = true
	step(operationalInput{event: opBatch, snapshot: true}, noticeNone)
	step(operationalInput{event: opTick}, noticeWaiting)
	want.connectionData, want.ready, want.lastUpdate, want.updates = true, true, at, 2
	step(operationalInput{event: opBatch, updates: 2}, noticeFirstData)
	step(operationalInput{event: opTick}, noticeNone)
	want.consumerFailures++
	step(operationalInput{event: opConsumerFailure}, noticeConsumerFailure)
	want.consumerFailures++
	step(operationalInput{event: opConsumerFailure}, noticeNone)
	at = at.Add(time.Second)
	want.connection, want.subscription, want.connectionData = false, false, false
	want.outage, want.outageStarted, want.outages = true, at, 1
	step(operationalInput{event: opOutage}, noticeOutage)
	want.retryAt = at.Add(4 * time.Second)
	step(operationalInput{event: opSchedule, delay: 4 * time.Second}, noticeProgress)
	at = at.Add(2 * time.Second)
	if state.currentOutageDuration(at) != 2*time.Second || state.nextDelay(at) != 2*time.Second {
		t.Fatal("incorrect outage progress")
	}
	step(operationalInput{event: opTick}, noticeProgress)
	at = at.Add(2 * time.Second)
	want.retryAt, want.attempts = time.Time{}, 1
	step(operationalInput{event: opAttempt}, noticeProgress)
	step(operationalInput{event: opOutage}, noticeNone)
	want.connection = true
	step(operationalInput{event: opConnected}, noticeNone)
	// Feed-before-completion counts envelopes but cannot establish recovery alone.
	want.updates, want.lastUpdate, want.connectionData = 3, at, true
	step(operationalInput{event: opBatch, updates: 1}, noticeNone)
	want.subscription, want.outage, want.outageStarted, want.recoveries = true, false, time.Time{}, 1
	want.closedOutageDuration = 4 * time.Second
	step(operationalInput{event: opBatch, snapshot: true}, noticeRecovered)
	step(operationalInput{event: opBatch, snapshot: true}, noticeNone)
	at = at.Add(time.Second)
	want.connection, want.subscription, want.connectionData = false, false, false
	want.outage, want.outageStarted, want.outages, want.stopped = true, at, 2, true
	step(operationalInput{event: opInvalid}, noticeInvalid)
	want.summarized = true
	step(operationalInput{event: opFinish}, noticeSummary)
	step(operationalInput{event: opFinish}, noticeNone)
	step(operationalInput{event: opTick}, noticeStopped)
	step(operationalInput{event: opConnected}, noticeNone)
	step(operationalInput{event: opBatch, snapshot: true, updates: 100}, noticeNone)
	step(operationalInput{event: opOutage}, noticeNone)
	step(operationalInput{event: opConsumerFailure}, noticeNone)
}

func TestOperationalRecoveryRequiresCurrentConnectionEvidence(t *testing.T) {
	at := time.Unix(1, 0)
	initial := operationalState{started: at, outageStarted: at, outage: true, outages: 1}
	for _, tc := range []struct {
		name    string
		events  []operationalInput
		want    operationalState
		notices []operationalNotice
	}{
		{"empty completion", []operationalInput{{event: opConnected}, {event: opBatch, snapshot: true}},
			operationalState{started: at, outageStarted: at, outage: true, outages: 1, connection: true, subscription: true}, []operationalNotice{noticeNone, noticeNone}},
		{"nonempty snapshot", []operationalInput{{event: opConnected}, {event: opBatch, snapshot: true, updates: 3}},
			operationalState{started: at, outages: 1, connection: true, subscription: true, connectionData: true, ready: true, recoveries: 1, updates: 3, lastUpdate: at}, []operationalNotice{noticeNone, noticeFirstData}},
		{"flapping loses feed evidence", []operationalInput{{event: opConnected}, {event: opBatch, updates: 1}, {event: opOutage}, {event: opConnected}, {event: opBatch, snapshot: true}},
			operationalState{started: at, outageStarted: at, outage: true, outages: 1, connection: true, subscription: true, updates: 1, lastUpdate: at}, []operationalNotice{noticeNone, noticeNone, noticeNone, noticeNone, noticeNone}},
		{"canceled backoff", []operationalInput{{event: opSchedule, delay: time.Second}, {event: opFinish}},
			operationalState{started: at, outageStarted: at, outage: true, outages: 1, stopped: true, summarized: true}, []operationalNotice{noticeProgress, noticeSummary}},
		{"source stopped", []operationalInput{{event: opSourceStopped}, {event: opFinish}},
			operationalState{started: at, outageStarted: at, outage: true, outages: 1, stopped: true, summarized: true}, []operationalNotice{noticeSourceStopped, noticeSummary}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initial
			var notices []operationalNotice
			for _, in := range tc.events {
				in.at = at
				var n operationalNotice
				s, n = reduceOperational(s, in)
				notices = append(notices, n)
			}
			if s != tc.want || !reflect.DeepEqual(notices, tc.notices) {
				t.Fatalf("got %+v / %v; want %+v / %v", s, notices, tc.want, tc.notices)
			}
		})
	}
}

func TestOperationalRetainsOutageDurations(t *testing.T) {
	origin := time.Unix(1000, 0)
	state := operationalState{}
	step := func(seconds int, event operationalEvent, updates int, want operationalState, notice operationalNotice, timing operationalDurations) {
		t.Helper()
		at := origin.Add(time.Duration(seconds) * time.Second)
		got, effect := reduceOperational(state, operationalInput{event: event, at: at, snapshot: event == opBatch, updates: updates})
		if got != want || effect != notice || operationalTiming(state, got, at) != timing {
			t.Fatalf("event %v at %d: state=%+v effect=%v timing=%+v; want %+v %v %+v", event, seconds, got, effect, operationalTiming(state, got, at), want, notice, timing)
		}
		state = got
	}
	want := operationalState{started: origin}
	step(0, opStart, 0, want, noticeStarting, operationalDurations{})
	want.connection = true
	step(0, opConnected, 0, want, noticeNone, operationalDurations{})
	want.subscription, want.connectionData, want.ready, want.updates, want.lastUpdate = true, true, true, 1, origin
	step(0, opBatch, 1, want, noticeFirstData, operationalDurations{})
	want.connection, want.subscription, want.connectionData = false, false, false
	want.outage, want.outages, want.outageStarted = true, 1, origin.Add(time.Hour)
	step(3600, opOutage, 0, want, noticeOutage, operationalDurations{run: time.Hour})
	want.connection = true
	step(3607, opConnected, 0, want, noticeNone, operationalDurations{3607 * time.Second, 7 * time.Second, 7 * time.Second})
	want.subscription, want.connectionData, want.outage, want.outageStarted = true, true, false, time.Time{}
	want.updates, want.recoveries, want.lastUpdate, want.closedOutageDuration = 2, 1, origin.Add(3610*time.Second), 10*time.Second
	step(3610, opBatch, 1, want, noticeRecovered, operationalDurations{3610 * time.Second, 10 * time.Second, 10 * time.Second})
	step(3620, opTick, 0, want, noticeNone, operationalDurations{run: 3620 * time.Second, totalOutage: 10 * time.Second})
	want.connection, want.subscription, want.connectionData = false, false, false
	want.outage, want.outages, want.outageStarted = true, 2, origin.Add(2*time.Hour)
	step(7200, opOutage, 0, want, noticeOutage, operationalDurations{run: 2 * time.Hour, totalOutage: 10 * time.Second})
	want.stopped, want.summarized = true, true
	step(7225, opFinish, 0, want, noticeSummary, operationalDurations{7225 * time.Second, 25 * time.Second, 35 * time.Second})
	step(7225, opFinish, 0, want, noticeNone, operationalDurations{7225 * time.Second, 25 * time.Second, 35 * time.Second})
}

func TestOperationalFirstDataClosesPriorOutageWithoutResumedClaim(t *testing.T) {
	at := time.Unix(1000, 0)
	before := operationalState{started: at.Add(-time.Hour), outage: true, outageStarted: at.Add(-12 * time.Second), outages: 1, connection: true}
	got, notice := reduceOperational(before, operationalInput{event: opBatch, at: at, snapshot: true, updates: 2})
	want := operationalState{started: before.started, connection: true, subscription: true, connectionData: true, ready: true, outages: 1, recoveries: 1, updates: 2, lastUpdate: at, closedOutageDuration: 12 * time.Second}
	if got != want || notice != noticeFirstData || operationalTiming(before, got, at) != (operationalDurations{time.Hour, 12 * time.Second, 12 * time.Second}) {
		t.Fatalf("first data closure = %+v / %v / %+v", got, notice, operationalTiming(before, got, at))
	}
}
