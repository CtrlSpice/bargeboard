package f1livetimingreceiver

import (
	"reflect"
	"sync"
	"testing"
	"testing/synctest"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
)

type orderedStatusHost struct {
	b                *statusBroadcaster
	entered, release chan struct{}
	mu               sync.Mutex
	events           []componentstatus.Status
}

func (*orderedStatusHost) GetExtensions() map[component.ID]component.Component { return nil }
func (h *orderedStatusHost) Report(e *componentstatus.Event) {
	// External reporting may query extensions. The broadcaster state lock must
	// not be held here, including during late-host replay.
	h.b.GetExtensions()
	if e.Status() == componentstatus.StatusRecoverableError {
		close(h.entered)
		<-h.release
	}
	h.mu.Lock()
	h.events = append(h.events, e.Status())
	h.mu.Unlock()
}

func TestStatusConcurrentReplayOrdering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &statusBroadcaster{}
		b.Report(componentstatus.NewRecoverableErrorEvent(errInputOutage))
		h := &orderedStatusHost{b: b, entered: make(chan struct{}), release: make(chan struct{})}
		registered := make(chan struct{})
		go func() { b.register(h); close(registered) }()
		<-h.entered
		if b.delivery.TryLock() {
			b.delivery.Unlock()
			close(h.release)
			<-registered
			t.Fatal("replay released delivery ordering before external report finished")
		}
		reported := make(chan struct{})
		go func() { b.Report(componentstatus.NewEvent(componentstatus.StatusOK)); close(reported) }()
		close(h.release)
		<-registered
		<-reported
		want := []componentstatus.Status{componentstatus.StatusRecoverableError, componentstatus.StatusOK}
		if !reflect.DeepEqual(h.events, want) {
			t.Fatalf("late host received %v, want %v", h.events, want)
		}
		late := &statusHost{events: make(chan *componentstatus.Event, 1)}
		b.register(late)
		if e := <-late.events; e.Status() != componentstatus.StatusOK {
			t.Fatalf("latest replay = %v", e.Status())
		}
	})
}

func TestStatusConcurrentBroadcastOrdering(t *testing.T) {
	b := &statusBroadcaster{}
	h := &orderedStatusHost{b: b, entered: make(chan struct{}), release: make(chan struct{})}
	b.register(h)
	first, second := make(chan struct{}), make(chan struct{})
	go func() { b.Report(componentstatus.NewRecoverableErrorEvent(errInputOutage)); close(first) }()
	<-h.entered
	if b.delivery.TryLock() {
		b.delivery.Unlock()
		close(h.release)
		<-first
		t.Fatal("broadcast released delivery ordering before external report finished")
	}
	go func() { b.Report(componentstatus.NewEvent(componentstatus.StatusOK)); close(second) }()
	close(h.release)
	<-first
	<-second
	if want := []componentstatus.Status{componentstatus.StatusRecoverableError, componentstatus.StatusOK}; !reflect.DeepEqual(h.events, want) {
		t.Fatalf("broadcast order = %v, want %v", h.events, want)
	}
}
