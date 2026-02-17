package entitymanager

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/entity"
)

type addEntityRequest struct {
	id            string
	entityType    reflect.Type
	stubFactoryFn spi.EntityStubFactoryFunc
	responseCh    chan error
}

type getEntityRequest struct {
	id         string
	responseCh chan getEntityResponse
}

type getEntityResponse struct {
	entityRecord EntityRecord
	err          error
}

type findEntitiesRequest struct {
	predicate  entity.Predicate
	responseCh chan findEntitiesResponse
}

type findEntitiesResponse struct {
	entities []EntityRecord
	err      error
}

type removeEntityRequest struct {
	registrationId string
	responseCh     chan error
}

type EntityRecord interface {
	GetId() string
	GetEntityType() reflect.Type
	GetStubFactoryFn() spi.EntityStubFactoryFunc
}
type entityRecord struct {
	id            string
	entityType    reflect.Type
	stubFactoryFn spi.EntityStubFactoryFunc
}

func (e entityRecord) GetId() string {
	return e.id
}

func (e entityRecord) GetEntityType() reflect.Type {
	return e.entityType
}

func (e entityRecord) GetStubFactoryFn() spi.EntityStubFactoryFunc {
	return e.stubFactoryFn
}

type EntityManager struct {
	canSendCh      chan bool
	addEntityCh    chan addEntityRequest
	getEntityCh    chan getEntityRequest
	findEntitiesCh chan findEntitiesRequest
	removeEntityCh chan removeEntityRequest
	runCancelFn    context.CancelFunc
	runDoneCh      chan error
	entities       map[string]entityRecord
}

func NewEntityManager() *EntityManager {
	entityManager := &EntityManager{
		canSendCh:      make(chan bool),
		addEntityCh:    make(chan addEntityRequest),
		getEntityCh:    make(chan getEntityRequest),
		findEntitiesCh: make(chan findEntitiesRequest),
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
		case request := <-em.getEntityCh:
			request.responseCh <- em.handleGetEntityRequest(request)
			close(request.responseCh)
			break
		case request := <-em.findEntitiesCh:
			request.responseCh <- em.handleFindEntitiesRequest(request)
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
	// Now that we've received the done signal, process the remaining requests that might already be in the channels
	for {
		select {
		case request := <-em.addEntityCh:
			request.responseCh <- em.handleAddEntityRequest(request)
			close(request.responseCh)
			break
		case request := <-em.getEntityCh:
			request.responseCh <- em.handleGetEntityRequest(request)
			close(request.responseCh)
			break
		case request := <-em.findEntitiesCh:
			request.responseCh <- em.handleFindEntitiesRequest(request)
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
	close(em.getEntityCh)
	close(em.removeEntityCh)

	fmt.Printf("EntityManager.Run stop\n")
}

func (em *EntityManager) handleAddEntityRequest(request addEntityRequest) error {
	_, ok := em.entities[request.id]

	if ok {
		return fmt.Errorf("an entity with id \"%s\" is already registered", request.id)
	}

	record := entityRecord{
		id:            request.id,
		entityType:    request.entityType,
		stubFactoryFn: request.stubFactoryFn,
	}

	em.entities[request.id] = record

	return nil
}

func (em *EntityManager) handleGetEntityRequest(request getEntityRequest) getEntityResponse {
	e, ok := em.entities[request.id]

	if !ok {
		return getEntityResponse{
			entityRecord: nil,
			err:          fmt.Errorf("no entity with id \"%s\" is registered", request.id),
		}
	}

	return getEntityResponse{
		entityRecord: e,
		err:          nil,
	}
}

func (em *EntityManager) handleFindEntitiesRequest(request findEntitiesRequest) findEntitiesResponse {
	entities := make([]EntityRecord, 0)

	for _, e := range em.entities {
		if request.predicate == nil {
			entities = append(entities, e)
		} else {
			info := entity.RegisteredEntityInfo{
				Id:            e.id,
				EntityType:    e.entityType,
				StubFactoryFn: e.stubFactoryFn,
			}

			if request.predicate(&info) {
				entities = append(entities, e)
			}
		}
	}

	return findEntitiesResponse{
		entities: entities,
		err:      nil,
	}
}

func (em *EntityManager) handleRemoveEntityRequest(request removeEntityRequest) error {
	_, ok := em.entities[request.registrationId]

	if !ok {
		return fmt.Errorf("no entity with id \"%s\" is registered", request.registrationId)
	}

	delete(em.entities, request.registrationId)

	return nil
}

func (em *EntityManager) AddEntity(
	id string,
	entityType reflect.Type,
	stubFactoryFn spi.EntityStubFactoryFunc) error {
	responseCh := make(chan error)
	request := addEntityRequest{
		id:            id,
		entityType:    entityType,
		stubFactoryFn: stubFactoryFn,
		responseCh:    responseCh}

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

func (em *EntityManager) GetEntity(id string) (EntityRecord, error) {
	responseCh := make(chan getEntityResponse)
	request := getEntityRequest{id: id, responseCh: responseCh}

	select {
	case <-em.canSendCh:
		close(responseCh)
		return nil, errors.New("unable to get, EntityManager is no longer accepting requests")
	case em.getEntityCh <- request:
		break
	}

	response := <-responseCh

	return response.entityRecord, response.err
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

func (em *EntityManager) FindEntities(predicate entity.Predicate) ([]EntityRecord, error) {
	responseCh := make(chan findEntitiesResponse)
	request := findEntitiesRequest{predicate: predicate, responseCh: responseCh}

	select {
	case <-em.canSendCh:
		close(responseCh)
		return nil, errors.New("unable to find, EntityManager is no longer accepting requests")
	case em.findEntitiesCh <- request:
		break
	}

	response := <-responseCh

	return response.entities, response.err
}
