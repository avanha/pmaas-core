package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

type addEntityRequest struct {
	id         string
	entityType reflect.Type
	responseCh chan error
}

type removeEntityRequest struct {
	registrationId string
	responseCh     chan error
}

type entityRecord struct {
	id         string
	entityType reflect.Type
}

type EntityManager struct {
	canSendCh      chan bool
	addEntityCh    chan addEntityRequest
	removeEntityCh chan removeEntityRequest
	runCancelFn    context.CancelFunc
	runDoneCh      chan error
	entities       map[string]entityRecord
}

func NewEntityManager() *EntityManager {
	entityManager := &EntityManager{
		canSendCh:      make(chan bool),
		addEntityCh:    make(chan addEntityRequest),
		removeEntityCh: make(chan removeEntityRequest),
		entities:       make(map[string]entityRecord),
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
			fmt.Printf("EntityManager terminated with error: %v\n", err)
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
			request.responseCh <- em.handleAddEntityRequest(request)
			close(request.responseCh)
			break
		case request := <-em.removeEntityCh:
			request.responseCh <- em.handleRemoveEntityRequest(request)
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
			request.responseCh <- em.handleAddEntityRequest(request)
			close(request.responseCh)
			break
		case request := <-em.removeEntityCh:
			request.responseCh <- em.handleRemoveEntityRequest(request)
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

func (em *EntityManager) handleAddEntityRequest(request addEntityRequest) error {
	_, ok := em.entities[request.id]

	if ok {
		return fmt.Errorf("an entity with id \"%s\" is already registered", request.id)
	}

	record := entityRecord{
		id:         request.id,
		entityType: request.entityType,
	}

	em.entities[request.id] = record

	return nil
}

func (em *EntityManager) handleRemoveEntityRequest(request removeEntityRequest) error {
	_, ok := em.entities[request.registrationId]

	if !ok {
		return fmt.Errorf("no entity with id \"%s\" is registered", request.registrationId)
	}

	delete(em.entities, request.registrationId)

	return nil
}

func (em *EntityManager) AddEntity(id string, entityType reflect.Type) error {
	responseCh := make(chan error)
	request := addEntityRequest{id: id, entityType: entityType, responseCh: responseCh}

	select {
	case <-em.canSendCh:
		close(responseCh)
		return errors.New("unable to add, EntityManager is no longer accepting requests")
	case em.addEntityCh <- request:
		break
	}

	err := <-responseCh

	return err
}

func (em *EntityManager) RemoveEntity(registrationId string) error {
	responseCh := make(chan error)
	request := removeEntityRequest{registrationId: registrationId, responseCh: responseCh}

	select {
	case <-em.canSendCh:
		close(responseCh)
		return errors.New("unable to remove, EntityManager is no longer accepting requests")
	case em.removeEntityCh <- request:
		break
	}

	return <-responseCh
}
