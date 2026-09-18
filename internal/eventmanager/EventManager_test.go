package eventmanager_test

import (
	"context"
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
		_ = em.Stop(context.Background())
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
		_ = em.Stop(context.Background())
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

	if err := em.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected error stopping EventManager: %v", err)
	}

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

// TestEventManager_Stop_ContextTimeout_StillCompletesShutdown ensures that when the
// caller's context expires before the shutdown sequence finishes, Stop returns an
// error but the shutdown (mailbox stop, dispatch channel close, dispatch drain)
// still runs to completion in the background, so no goroutine is leaked and a
// subsequent Stop call is a safe no-op.
func TestEventManager_Stop_ContextTimeout_StillCompletesShutdown(t *testing.T) {
	em := eventmanager.NewEventManager()

	if err := em.Start(); err != nil {
		t.Fatalf("unexpected error starting EventManager: %v", err)
	}

	// Use an already-expired context so Stop returns immediately via the
	// ctx.Done() branch, before the background shutdown goroutine has any
	// realistic chance to finish.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := em.Stop(ctx); err == nil {
		t.Fatalf("expected error stopping EventManager with an expired context, got nil")
	}

	// Regardless of the timeout, the background shutdown goroutine must
	// eventually complete on its own and leave the EventManager fully stopped.
	// Poll BroadcastEvent, since the shutdown runs asynchronously and isn't
	// guaranteed to have finished the instant Stop returns.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := em.BroadcastEvent(reflect.TypeOf(struct{}{}), "entity-1", "hello")
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected shutdown to eventually complete and reject requests, but it never did")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A subsequent Stop call (even with a fresh, non-expired context) must be a
	// safe no-op and must not block.
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- em.Stop(context.Background())
	}()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("unexpected error on subsequent Stop call: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("subsequent Stop call did not return in time; shutdown may not have completed")
	}
}
