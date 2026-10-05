package eventmanager_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/avanha/pmaas-core/config"
	"github.com/avanha/pmaas-core/internal/eventmanager"
	"github.com/avanha/pmaas-core/internal/plugins"
	"github.com/avanha/pmaas-spi/events"
)

func newTestPluginWrapper() *plugins.PluginWrapper {
	pw := plugins.NewPluginWrapper(nil, config.PluginWithConfig{})
	pw.StartPluginRunner()
	return pw
}

func TestEventManager_AddReceiver_BroadcastEvent_RemoveReceiver(t *testing.T) {
	em := eventmanager.NewEventManager()

	if err := em.Start(); err != nil {
		t.Fatalf("unexpected error starting EventManager: %v", err)
	}
	defer func() {
		em.Stop()
	}()

	pw := newTestPluginWrapper()
	defer pw.StopPluginRunner()

	receivedCh := make(chan *events.EventInfo, 1)
	handle, err := em.AddReceiver(
		pw,
		func(eventInfo *events.EventInfo) bool { return true },
		func(eventInfo *events.EventInfo) error {
			receivedCh <- eventInfo
			return nil
		})

	if err != nil {
		t.Fatalf("unexpected error adding receiver: %v", err)
	}

	if err := em.BroadcastEvent(reflect.TypeOf(struct{}{}), "entity-1", "hello"); err != nil {
		t.Fatalf("unexpected error broadcasting event: %v", err)
	}

	select {
	case eventInfo := <-receivedCh:
		if eventInfo.SourceEntityId != "entity-1" || eventInfo.Event != "hello" {
			t.Fatalf("unexpected event info: %+v", eventInfo)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for event to be received")
	}

	if err := em.RemoveReceiver(handle); err != nil {
		t.Fatalf("unexpected error removing receiver: %v", err)
	}

	if err := em.BroadcastEvent(reflect.TypeOf(struct{}{}), "entity-1", "hello again"); err != nil {
		t.Fatalf("unexpected error broadcasting event: %v", err)
	}

	select {
	case eventInfo := <-receivedCh:
		t.Fatalf("did not expect a removed receiver to be invoked, got: %+v", eventInfo)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestEventManager_RemoveReceiver_UnknownHandle(t *testing.T) {
	em := eventmanager.NewEventManager()

	if err := em.Start(); err != nil {
		t.Fatalf("unexpected error starting EventManager: %v", err)
	}
	defer func() {
		em.Stop()
	}()

	if err := em.RemoveReceiver(42); err == nil {
		t.Fatalf("expected error removing an unknown receiver handle, got nil")
	}
}

func TestEventManager_Stop_RejectsFurtherRequests(t *testing.T) {
	em := eventmanager.NewEventManager()

	if err := em.Start(); err != nil {
		t.Fatalf("unexpected error starting EventManager: %v", err)
	}

	em.Stop()

	if err := em.BroadcastEvent(reflect.TypeOf(struct{}{}), "entity-1", "hello"); err == nil {
		t.Fatalf("expected error broadcasting event after stop, got nil")
	}

	pw := newTestPluginWrapper()
	defer pw.StopPluginRunner()

	if _, err := em.AddReceiver(pw, func(*events.EventInfo) bool { return true }, func(*events.EventInfo) error { return nil }); err == nil {
		t.Fatalf("expected error adding receiver after stop, got nil")
	}

	if err := em.RemoveReceiver(1); err == nil {
		t.Fatalf("expected error removing receiver after stop, got nil")
	}
}

func TestEventManager_Stop_IsIdempotent(t *testing.T) {
	em := eventmanager.NewEventManager()

	if err := em.Start(); err != nil {
		t.Fatalf("unexpected error starting EventManager: %v", err)
	}

	em.Stop()

	doneCh := make(chan struct{})
	go func() {
		em.Stop()
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("a repeat Stop call did not return")
	}
}

// TestEventManager_Stop_WaitsForAnInFlightReceiver pins down that Stop has no timeout: dispatch is
// serialized, so a receiver that doesn't return holds Stop up for as long as it takes. That's
// deliberate, a receiver that never returns is a bug to be found rather than waited out, and a
// timeout would only hide it.
func TestEventManager_Stop_WaitsForAnInFlightReceiver(t *testing.T) {
	em := eventmanager.NewEventManager()

	if err := em.Start(); err != nil {
		t.Fatalf("unexpected error starting EventManager: %v", err)
	}

	pw := newTestPluginWrapper()
	defer pw.StopPluginRunner()

	inReceiver := make(chan struct{})
	releaseReceiver := make(chan struct{})

	_, err := em.AddReceiver(
		pw,
		func(*events.EventInfo) bool { return true },
		func(*events.EventInfo) error {
			close(inReceiver)
			<-releaseReceiver
			return nil
		})
	if err != nil {
		t.Fatalf("unexpected error adding receiver: %v", err)
	}

	if err := em.BroadcastEvent(reflect.TypeOf(struct{}{}), "entity-1", "hello"); err != nil {
		t.Fatalf("unexpected error broadcasting event: %v", err)
	}

	select {
	case <-inReceiver:
	case <-time.After(2 * time.Second):
		t.Fatalf("receiver was never invoked")
	}

	stopped := make(chan struct{})
	go func() {
		em.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatalf("Stop returned while a receiver was still running")
	case <-time.After(300 * time.Millisecond):
	}

	close(releaseReceiver)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatalf("Stop did not return once the receiver finished")
	}

	if err := em.BroadcastEvent(reflect.TypeOf(struct{}{}), "entity-1", "again"); err == nil {
		t.Fatalf("expected error broadcasting event after stop, got nil")
	}
}
