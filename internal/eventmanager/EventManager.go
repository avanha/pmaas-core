package eventmanager

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"

	"github.com/avanha/pmaas-common/queue"
	"github.com/avanha/pmaas-core/internal/mailbox"
	"github.com/avanha/pmaas-core/internal/plugins"
	"github.com/avanha/pmaas-spi/events"
)

type broadcastEventRequest struct {
	sourcePluginType reflect.Type
	sourceEntityId   string
	event            any
}

func (r broadcastEventRequest) String() string {
	return fmt.Sprintf("%T from %v %v %v", r.event, r.sourcePluginType, r.sourceEntityId, r.event)
}

type dispatchRequest struct {
	receiver  events.EventReceiver
	handle    int
	plugin    *plugins.PluginWrapper
	eventInfo *events.EventInfo
}

type receiverRecord struct {
	handle    int
	plugin    *plugins.PluginWrapper
	predicate events.EventPredicate
	receiver  events.EventReceiver
}

type EventManager struct {
	mailbox            *mailbox.Mailbox
	addReceiverCounter int
	receivers          map[int]receiverRecord
	dispatchEventCh    *queue.UnboundedChannel[dispatchRequest]
	dispatchDoneCh     chan error

	// Guards Stop so that only the first call performs the shutdown sequence.
	stopped atomic.Bool
}

func NewEventManager() *EventManager {
	eventManager := &EventManager{
		receivers: make(map[int]receiverRecord),
	}

	return eventManager
}

func (em *EventManager) Start() error {
	fmt.Printf("Event manager starting\n")
	em.mailbox = mailbox.NewMailbox()
	em.dispatchEventCh = queue.NewUnboundedChannel[dispatchRequest]()
	em.dispatchDoneCh = make(chan error)

	// Execution of registered listeners is done in a separate GoRoutine to allow
	// event receivers to perform register/deregister operations.
	go dispatchEvents(em.dispatchEventCh.Out(), em.dispatchDoneCh)

	return nil
}

func (em *EventManager) Stop(ctx context.Context) error {
	if em.mailbox == nil {
		return nil
	}

	// Safe to call multiple times; only the first call does anything.
	if !em.stopped.CompareAndSwap(false, true) {
		fmt.Printf("Event manager already stoppingx\n")
		return nil
	}

	fmt.Printf("Event manager stopping\n")

	// Run the full shutdown sequence (mailbox stop, dispatch channel close, dispatch
	// drain) to completion in a background goroutine, regardless of whether the
	// caller's context expires while waiting.  This ensures that a context timeout
	// only affects whether Stop blocks the caller; it never leaves the shutdown
	// half-finished or leaks the dispatcher goroutine.
	stoppedCh := make(chan struct{})
	go func() {
		em.mailbox.Stop()

		// Signal the dispatcher GoRoutine to stop and wait for it to terminate
		em.dispatchEventCh.Close()
		dispatchErr := <-em.dispatchDoneCh

		if dispatchErr != nil {
			fmt.Printf("EventManager terminated with error: %v\n", dispatchErr)
		}

		close(stoppedCh)
		fmt.Printf("Event manager stopped\n")
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("error stopping EventManager, context done signal received while waiting for termination: %v", ctx.Err())
	case <-stoppedCh:
		return nil
	}
}

func (em *EventManager) BroadcastEvent(sourcePluginType reflect.Type, sourceEntityId string, event any) error {
	request := broadcastEventRequest{
		sourceEntityId:   sourceEntityId,
		sourcePluginType: sourcePluginType,
		event:            event,
	}

	err := em.mailbox.Send(func() { em.handleBroadcastEvent(request) })
	if err != nil {
		return errors.New("unable to broadcast event, EventManager is no longer accepting requests")
	}

	return nil
}

func (em *EventManager) AddReceiver(
	plugin *plugins.PluginWrapper,
	predicate events.EventPredicate,
	receiver events.EventReceiver) (int, error) {
	handle, err := em.mailbox.Exec(func() int {
		return em.handleAddReceiver(plugin, predicate, receiver)
	})
	if err != nil {
		return 0, errors.New("unable to add event receiver, EventManager is no longer accepting requests")
	}

	return handle, nil
}

func (em *EventManager) RemoveReceiver(receiverHandle int) error {
	removeErr, err := em.mailbox.Exec(func() error {
		return em.handleRemoveReceiver(receiverHandle)
	})
	if err != nil {
		return errors.New("unable to remove event receiver, EventManager is no longer accepting requests")
	}

	return removeErr
}

// handleBroadcastEvent Delivers an event to registered receivers.  It executes each registration's predicate, and
// if the predicate returns true, executes the registration's callback function via the plugin's plugin runner
// goroutine.  Since the predicate executes directly, it's imperative that it's fast.
func (em *EventManager) handleBroadcastEvent(request broadcastEventRequest) {
	fmt.Printf("EventManager: Broadcasting event, %v\n", request)

	eventInfo := &events.EventInfo{
		SourceEntityId:   request.sourceEntityId,
		SourcePluginType: request.sourcePluginType,
		Event:            request.event,
	}

	for _, record := range em.receivers {
		if record.predicate(eventInfo) {
			em.dispatchEventCh.In() <- dispatchRequest{
				eventInfo: eventInfo,
				receiver:  record.receiver,
				handle:    record.handle,
				plugin:    record.plugin,
			}
		}
	}
}

func (em *EventManager) handleAddReceiver(
	plugin *plugins.PluginWrapper,
	predicate events.EventPredicate,
	receiver events.EventReceiver) int {
	// It would be better to specify the event types directly, instead of relying only on the
	// predicate, that way we can use maps per event type, rather than having to scan and test
	// all registered receivers.  It should support a list of event types, as well as an "any"
	// event type wild card.
	handle := em.addReceiverCounter + 1
	em.addReceiverCounter = handle
	record := receiverRecord{
		handle:    handle,
		plugin:    plugin,
		predicate: predicate,
		receiver:  receiver}
	em.receivers[handle] = record
	return handle
}

func (em *EventManager) handleRemoveReceiver(receiverHandle int) error {
	_, ok := em.receivers[receiverHandle]

	if !ok {
		return fmt.Errorf("receiver handle %v not found", receiverHandle)
	}

	delete(em.receivers, receiverHandle)
	return nil
}

func dispatchEvents(dispatchRequestCh <-chan dispatchRequest, doneCh chan error) {
	defer close(doneCh)

	for request := range dispatchRequestCh {
		err := request.plugin.ExecErrorFn(func() error { return request.receiver(request.eventInfo) })

		if err != nil {
			fmt.Printf(
				"EventManager: Event receiver %d returned error when processing %v\n",
				request.handle,
				*request.eventInfo)
		}
	}
}
