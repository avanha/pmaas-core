package core

import (
	"context"
	"errors"
	"fmt"
	"pmaas.io/spi/events"
	"reflect"
)

type broadcastEventRequest struct {
	eventSource reflect.Type
	event       any
}

func (r broadcastEventRequest) String() string {
	return fmt.Sprintf("%T from %v %v", r.event, r.eventSource, r.event)
}

type addReceiverRequest struct {
	predicate events.EventPredicate
	receiver  events.EventReceiver
	resultCh  chan int
}

type removeReceiverRequest struct {
	receiverHandle int
	resultCh       chan error
}

type receiverRecord struct {
	handle    int
	predicate events.EventPredicate
	receiver  events.EventReceiver
}

type EventManager struct {
	runCancelFn        context.CancelFunc
	runDoneCh          chan error
	runningCh          chan bool
	broadcastEventCh   chan broadcastEventRequest
	addReceiverCh      chan addReceiverRequest
	removeReceiverCh   chan removeReceiverRequest
	addReceiverCounter int
	receivers          map[int]receiverRecord
}

func NewEventManager() *EventManager {
	eventManager := &EventManager{
		receivers: make(map[int]receiverRecord),
	}

	return eventManager
}

func (em *EventManager) Start() error {
	ctx, cancelFn := context.WithCancel(context.Background())
	doneCh := make(chan error)
	go em.run(ctx, doneCh)
	em.runningCh = make(chan bool)
	em.broadcastEventCh = make(chan broadcastEventRequest)
	em.addReceiverCh = make(chan addReceiverRequest)
	em.removeReceiverCh = make(chan removeReceiverRequest)
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

func (em *EventManager) BroadcastEvent(sourceType reflect.Type, event any) error {
	select {
	case <-em.runningCh:
		return errors.New("unable to broadcast event, EventManager is no longer accepting requests")
	case em.broadcastEventCh <- broadcastEventRequest{eventSource: sourceType, event: event}:
		break
	}

	return nil
}

func (em *EventManager) AddReceiver(predicate events.EventPredicate, receiver events.EventReceiver) (int, error) {
	resultCh := make(chan int)

	select {
	case <-em.runningCh:
		close(resultCh)
		return 0, errors.New("unable to add event receiver, EventManager is no longer accepting requests")
	case em.addReceiverCh <- addReceiverRequest{predicate: predicate, receiver: receiver, resultCh: resultCh}:
		break
	}

	receiverHandle := <-resultCh

	return receiverHandle, nil
}

func (em *EventManager) RemoveReceiver(receiverHandle int) error {
	resultCh := make(chan error)

	select {
	case <-em.runningCh:
		close(resultCh)
		return errors.New("unable to remove event receiver, EventManager is no longer accepting requests")
	case em.removeReceiverCh <- removeReceiverRequest{receiverHandle: receiverHandle, resultCh: resultCh}:
		break
	}

	return <-resultCh
}

func (em *EventManager) run(ctx context.Context, doneCh chan error) {
	fmt.Printf("EventManager.Run start\n")
	defer func() { close(doneCh) }()

LOOP1:
	// Process requests until we receive the done signal
	for {
		select {
		case request := <-em.broadcastEventCh:
			em.handleBroadcastEvent(request)
			break
		case request := <-em.addReceiverCh:
			em.handleAddReceiver(&request)
			break
		case request := <-em.removeReceiverCh:
			em.handleRemoveReceiver(&request)
			break
		case <-ctx.Done():
			fmt.Printf("EventManager: ctx.Done signalled\n")
			// Close running, which will prevent any more requests
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
		case request := <-em.addReceiverCh:
			em.handleAddReceiver(&request)
			break
		default:
			break LOOP2
		}
	}

	close(em.broadcastEventCh)

	fmt.Printf("EventManager.Run stop\n")
}

func (em *EventManager) handleBroadcastEvent(request broadcastEventRequest) {
	fmt.Printf("EventManager: Broadcasting event, %v\n", request)

	eventInfo := &events.EventInfo{
		EventSourceType: request.eventSource,
		Event:           request.event,
	}

	for _, record := range em.receivers {
		if record.predicate(eventInfo) {
			err := record.receiver(eventInfo)

			if err != nil {
				fmt.Printf(
					"EventManager: Event receiver %d returned error when processing %v\n",
					record.handle,
					*eventInfo)
			}
		}
	}
}

func (em *EventManager) handleAddReceiver(request *addReceiverRequest) {
	// It would be better to specify the event types directly, instead of relying only on the
	// predicate, that way we can use maps per event type, rather than having to scan and test
	// all registered receivers.  It should support a list of event types, as well as an "any"
	// event type wild card.
	handle := em.addReceiverCounter + 1
	em.addReceiverCounter = handle
	record := receiverRecord{handle: handle, predicate: request.predicate, receiver: request.receiver}
	em.receivers[handle] = record
	request.resultCh <- handle
}

func (em *EventManager) handleRemoveReceiver(request *removeReceiverRequest) {
	_, ok := em.receivers[request.receiverHandle]

	if !ok {
		request.resultCh <- errors.New(fmt.Sprintf("Receiver handle %v not found", request.receiverHandle))
		return
	}

	delete(em.receivers, request.receiverHandle)
	request.resultCh <- nil
}
