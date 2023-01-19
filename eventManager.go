package core

import (
	"context"
	"errors"
	"fmt"
)

type EventManager struct {
	runCancelFn      context.CancelFunc
	runDoneCh        chan error
	runningCh        chan bool
	broadcastEventCh chan any
}

func NewEventManager() *EventManager {
	eventManager := &EventManager{}

	return eventManager
}

func (em *EventManager) Start() error {
	ctx, cancelFn := context.WithCancel(context.Background())
	doneCh := make(chan error)
	go em.run(ctx, doneCh)
	em.runningCh = make(chan bool)
	em.broadcastEventCh = make(chan any)
	em.runCancelFn = cancelFn
	em.runDoneCh = doneCh
	return nil
}

func (em *EventManager) Stop(ctx context.Context) error {
	if em.runCancelFn == nil {
		return nil
	}

	runCancelFn := em.runCancelFn
	em.runCancelFn = nil

	runCancelFn()

	select {
	case <-ctx.Done():
		return fmt.Errorf("error stopping EventManager, context done signal received while waiting for termination: %v", ctx.Err())
	case err := <-em.runDoneCh:
		if err != nil {
			fmt.Printf("EventManager terminated with error: %v\n", err)
		}
		return nil
	}
}

func (em *EventManager) BroadcastEvent(event any) error {
	select {
	case <-em.runningCh:
		return errors.New("unable to broadcast event, EventManager is no longer accepting requests")
	case em.broadcastEventCh <- event:
		break
	}

	return nil
}

func (em *EventManager) run(ctx context.Context, doneCh chan error) {
	fmt.Printf("EventManager.Run start\n")
	defer func() { close(doneCh) }()

LOOP1:
	// Process requests until we receive the done signal
	for {
		select {
		case event := <-em.broadcastEventCh:
			em.handleBroadcastEvent(event)
			break
		case <-ctx.Done():
			fmt.Printf("EventManager: ctx.Done signalled\n")
			// Close canSendCh, which will prevent any more requests
			close(em.runningCh)
			break LOOP1
		}
	}

	// Consume any events already queued
LOOP2:
	for {
		select {
		case event := <-em.broadcastEventCh:
			em.handleBroadcastEvent(event)
			break
		default:
			break LOOP2
		}
	}

	close(em.broadcastEventCh)

	fmt.Printf("EventManager.Run stop\n")
}

func (em *EventManager) handleBroadcastEvent(event any) {
	fmt.Printf("EventManager: Broadcasting event, %v\n", event)
}
