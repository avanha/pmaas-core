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
	plugin    *pluginWithConfig
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
	plugin    *pluginWithConfig
	predicate events.EventPredicate
	receiver  events.EventReceiver
}

type EventManager struct {
	runCancelFn context.CancelFunc
	runDoneCh   chan error
	runningCh   chan bool
	//broadcastEventCh   *chann.Chann[broadcastEventRequest]
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
	//em.broadcastEventCh = chann.New[broadcastEventRequest]()
	em.broadcastEventCh = make(chan broadcastEventRequest, 100)
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
	// When using an unbuffered chanel, we get a deadlock if an event receiver tries to broadcast an event.
	// This happens because the event manager goroutine is already busy performing the dispatch, so it
	// can't receive the request.  We could create a buffered channel, but there's no guarantee about
	// hitting the buffer limit.  We could save broadcast calls to a queue and process the queue at the end
	// of dispatch.  Or fire off a goroutine to enqueue them. I'm going to use xchann unbounded queue.
	// Update: The xchan implementation has some sort of race condition that makes it occasionally crash
	// on startup.  I'm gonna switch back to a regular buffered channel.
	select {
	case <-em.runningCh:
		return errors.New("unable to broadcast event, EventManager is no longer accepting requests")
	//case em.broadcastEventCh.In() <- broadcastEventRequest{eventSource: sourceType, event: event}:
	case em.broadcastEventCh <- broadcastEventRequest{eventSource: sourceType, event: event}:
		break
	}

	return nil
}

func (em *EventManager) AddReceiver(
	plugin *pluginWithConfig,
	predicate events.EventPredicate,
	receiver events.EventReceiver) (int, error) {
	resultCh := make(chan int)

	select {
	case <-em.runningCh:
		close(resultCh)
		return 0, errors.New("unable to add event receiver, EventManager is no longer accepting requests")
	case em.addReceiverCh <- addReceiverRequest{plugin: plugin, predicate: predicate, receiver: receiver, resultCh: resultCh}:
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
	fmt.Printf("EventManager.run: start\n")
	fmt.Printf("EventManager.run: Select requests or done signal\n")
	defer func() { close(doneCh) }()

	// Process requests until we receive the done signal
	for run := true; run; {
		select {
		//case request := <-em.broadcastEventCh.Out():
		case request := <-em.broadcastEventCh:
			//fmt.Printf("EventManager.run: handling broadcast event request\n")
			em.handleBroadcastEvent(request)
			break
		case request := <-em.addReceiverCh:
			//fmt.Printf("EventManager.run: handling add receiver request\n")
			em.handleAddReceiver(&request)
			break
		case request := <-em.removeReceiverCh:
			//fmt.Printf("EventManager.run: handling remove receiver request\n")
			em.handleRemoveReceiver(&request)
			break
		case <-ctx.Done():
			fmt.Printf("EventManager.run: ctx.Done signalled\n")
			// Close running, which will prevent any more requests
			close(em.runningCh)
			run = false
			break
		}
		//fmt.Printf("EventManager.run: Select requests or done signal\n")
	}

	fmt.Printf("EventManager.run: Handling remaining requests\n")

	// Consume any events already queued

	for run := true; run; {
		select {
		//case event := <-em.broadcastEventCh.Out():
		case event := <-em.broadcastEventCh:
			em.handleBroadcastEvent(event)
			break
		case request := <-em.addReceiverCh:
			em.handleAddReceiver(&request)
			break
		case request := <-em.removeReceiverCh:
			em.handleRemoveReceiver(&request)
			break
		default:
			run = false
			break
		}
		//fmt.Printf("EventManager.run: Select requests\n")
	}

	fmt.Printf("EventManager.run: stop\n")
}

// handleBroadcastEvent Delivers an event to registered receivers.  It executes each registration's predicate, and
// if the predicate returns true, executes the registration's callback function via the plugin's plugin runner
// goroutine.  Since the predicate executes directly, it's imperative that it's fast.
func (em *EventManager) handleBroadcastEvent(request broadcastEventRequest) {
	fmt.Printf("EventManager: Broadcasting event, %v\n", request)

	eventInfo := &events.EventInfo{
		EventSourceType: request.eventSource,
		Event:           request.event,
	}

	for _, record := range em.receivers {
		if record.predicate(eventInfo) {
			err := record.plugin.execErrorFn(func() error { return record.receiver(eventInfo) })

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
	record := receiverRecord{
		handle:    handle,
		plugin:    request.plugin,
		predicate: request.predicate,
		receiver:  request.receiver}
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
