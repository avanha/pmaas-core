package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type addEntityRequest struct {
	entityInfo string
	responseCh chan string
}

type removeEntityRequest struct {
	registrationId string
	responseCh     chan string
}

type EntityManager struct {
	canSendCh      chan bool
	addEntityCh    chan addEntityRequest
	removeEntityCh chan removeEntityRequest
	runCancelFn    context.CancelFunc
	runDoneCh      chan error
}

func NewEntityManager() *EntityManager {
	entityManager := &EntityManager{
		canSendCh:      make(chan bool),
		addEntityCh:    make(chan addEntityRequest),
		removeEntityCh: make(chan removeEntityRequest),
	}
	return entityManager
}

func (em *EntityManager) Start() error {
	ctx, cancelFn := context.WithCancel(context.Background())
	doneCh := make(chan error)
	go em.run(ctx, doneCh)
	em.runCancelFn = cancelFn
	em.runDoneCh = doneCh
	return nil
}

func (em *EntityManager) Stop(ctx context.Context) error {
	if em.runCancelFn == nil {
		return nil
	}

	runCancelFn := em.runCancelFn
	em.runCancelFn = nil

	runCancelFn()

	select {
	case <-ctx.Done():
		return fmt.Errorf("error stopping EntityManager, context done signal received while waiting for termination: %v", ctx.Err())
	case err := <-em.runDoneCh:
		if err != nil {
			fmt.Printf("EntityManager terminated with error: %v", err)
		}
		return nil
	}
}

func (em *EntityManager) run(ctx context.Context, doneCh chan error) {
	fmt.Printf("EntityManager.Run start\n")
	defer func() { close(doneCh) }()
LOOP1:
	// Process requests until we receive the done signal
	for {
		select {
		case request := <-em.addEntityCh:
			request.responseCh <- fmt.Sprintf("entity_%s", time.Now().GoString())
			close(request.responseCh)
			break
		case request := <-em.removeEntityCh:
			request.responseCh <- request.registrationId
			close(request.responseCh)
			break
		case <-ctx.Done():
			fmt.Printf("EntityManager: ctx.Done signalled\n")
			// Close canSendCh, which will prevent any more requests
			close(em.canSendCh)
			break LOOP1
		}
	}
LOOP2:
	// Now that we've received the done signal process the remaining requests that might already be in the channels
	for {
		select {
		case request := <-em.addEntityCh:
			request.responseCh <- fmt.Sprintf("entity_%s", time.Now().GoString())
			close(request.responseCh)
			break
		case request := <-em.removeEntityCh:
			request.responseCh <- request.registrationId
			close(request.responseCh)
			break
		default:
			// Channels are empty
			break LOOP2
		}
	}

	// All requests have been processed, close the channels
	close(em.addEntityCh)
	close(em.removeEntityCh)

	fmt.Printf("EntityManager.Run stop\n")
}

func (em *EntityManager) AddEntity(temp string) (string, error) {
	responseCh := make(chan string)
	request := addEntityRequest{entityInfo: temp, responseCh: responseCh}

	select {
	case <-em.canSendCh:
		close(responseCh)
		return "", errors.New("unable to add, EntityManager is no longer accepting requests")
	case em.addEntityCh <- request:
		break
	}

	id := <-responseCh

	return id, nil

}

func (em *EntityManager) RemoveEntity(registrationId string) error {
	responseCh := make(chan string)
	request := removeEntityRequest{registrationId: registrationId, responseCh: responseCh}

	select {
	case <-em.canSendCh:
		close(responseCh)
		return errors.New("unable to remove, EntityManager is no longer accepting requests")
	case em.removeEntityCh <- request:
		break
	}

	<-responseCh

	return nil
}
