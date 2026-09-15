package f1livetimingreceiver

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPayloadQualityOperationalReduction(t *testing.T) {
	at := time.Unix(100, 0)
	state := operationalState{started: at, connection: true}
	want := state
	step := func(in operationalInput, notice operationalNotice) {
		t.Helper()
		in.at = at
		got, effect := reduceOperational(state, in)
		if got != want || effect != notice {
			t.Fatalf("event=%v: state=%+v notice=%v; want %+v / %v", in.event, got, effect, want, notice)
		}
		state = got
	}
	step(operationalInput{event: opPeriodicTick}, noticeWaiting)
	want.subscription, want.connectionData, want.ready = true, true, true
	want.updates, want.lastUpdate = 3, at
	want.invalidUnicodeUpdates, want.reportedInvalidUnicodeUpdates = 2, 2
	step(operationalInput{event: opBatch, snapshot: true, updates: 3, invalidUnicodeUpdates: 2}, noticeFirstData)
	want.updates, want.invalidUnicodeUpdates = 5, 4
	step(operationalInput{event: opBatch, updates: 2, invalidUnicodeUpdates: 2}, noticeNone)
	// Retry progress uses opTick too; it must not flush the quality cadence.
	step(operationalInput{event: opTick}, noticeNone)
	want.consumerFailures = 1
	step(operationalInput{event: opConsumerFailure}, noticeConsumerFailure)
	want.updates = 6
	step(operationalInput{event: opBatch, updates: 1}, noticeNone)
	want.reportedInvalidUnicodeUpdates = 4
	step(operationalInput{event: opPeriodicTick}, noticeNone)
	step(operationalInput{event: opPeriodicTick}, noticeNone)
	want.connection, want.subscription, want.connectionData = false, false, false
	want.outage, want.outageStarted, want.outages = true, at, 1
	step(operationalInput{event: opOutage}, noticeOutage)
	want.connection = true
	step(operationalInput{event: opConnected}, noticeNone)
	want.updates, want.invalidUnicodeUpdates, want.connectionData = 7, 5, true
	step(operationalInput{event: opBatch, updates: 1, invalidUnicodeUpdates: 1}, noticeNone)
	// Quality reporting and transport progress coexist; neither erases the other.
	want.reportedInvalidUnicodeUpdates = 5
	step(operationalInput{event: opPeriodicTick}, noticeProgress)
	want.subscription, want.outage, want.outageStarted, want.recoveries = true, false, time.Time{}, 1
	step(operationalInput{event: opBatch, snapshot: true}, noticeRecovered)
	want.updates, want.invalidUnicodeUpdates = 8, 6
	step(operationalInput{event: opBatch, updates: 1, invalidUnicodeUpdates: 1}, noticeNone)
	want.connection, want.subscription, want.connectionData, want.stopped = false, false, false, true
	want.outage, want.outageStarted, want.outages = true, at, 2
	step(operationalInput{event: opInvalid}, noticeInvalid)
	want.summarized = true
	step(operationalInput{event: opFinish}, noticeSummary)
	step(operationalInput{event: opFinish}, noticeNone)
	step(operationalInput{event: opBatch, updates: 100, invalidUnicodeUpdates: 100}, noticeNone)
	step(operationalInput{event: opPeriodicTick}, noticeStopped)
	step(operationalInput{event: opTick}, noticeStopped)
}

func TestPayloadQualityFirstFindingDuringTransportRecovery(t *testing.T) {
	at := time.Unix(100, 0)
	before := operationalState{started: at.Add(-time.Hour), ready: true, outage: true, outageStarted: at.Add(-time.Second), outages: 1, connection: true, updates: 3, lastUpdate: at.Add(-time.Minute)}
	got, notice := reduceOperational(before, operationalInput{event: opBatch, at: at, snapshot: true, updates: 2, invalidUnicodeUpdates: 1})
	want := operationalState{started: before.started, ready: true, connection: true, subscription: true, connectionData: true, outages: 1, recoveries: 1, closedOutageDuration: time.Second,
		updates: 5, lastUpdate: at, invalidUnicodeUpdates: 1, reportedInvalidUnicodeUpdates: 1}
	if got != want || notice != noticeRecovered {
		t.Fatalf("state=%+v notice=%v; want %+v / %v", got, notice, want, noticeRecovered)
	}
}

func TestPayloadQualityResultShapesStayBounded(t *testing.T) {
	for _, tc := range []struct {
		value  any
		fields string
	}{
		{normalizedLiveTimingBatch{}, "source requestedTopics presentTopics observationTime updates invalidUnicodeUpdates"},
		{normalizedLiveTimingUpdate{}, "topic payload timestamp source"},
		{operationalState{}, "started outageStarted lastUpdate retryAt connection subscription connectionData ready outage stopped summarized outages attempts recoveries updates consumerFailures invalidUnicodeUpdates reportedInvalidUnicodeUpdates closedOutageDuration"},
		{operationalInput{}, "event at delay notBefore setupStage httpStatus updates invalidUnicodeUpdates snapshot"},
	} {
		typeOf := reflect.TypeOf(tc.value)
		var fields []string
		for i := 0; i < typeOf.NumField(); i++ {
			fields = append(fields, typeOf.Field(i).Name)
		}
		if strings.Join(fields, " ") != tc.fields {
			t.Fatalf("review complete oracle for %s: %v", typeOf, fields)
		}
	}
}
