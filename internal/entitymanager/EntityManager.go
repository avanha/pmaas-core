package entitymanager

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/avanha/pmaas-common/mailbox"
	"github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/entity"
)

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

type getEntityResponse struct {
	entityRecord EntityRecord
	err          error
}

type findEntitiesResponse struct {
	entities []EntityRecord
	err      error
}

// EntityManager tracks registered entities in a plain map, guarded entirely by running every access
// through its own Mailbox — the same actor pattern EventManager uses for its receivers map, in place of
// EntityManager's previous hand-rolled request-channel/canSendCh mechanism. That mechanism's shutdown
// had a real race (closing a "no longer accepting" signal channel before waiting for in-flight callers
// to observe it, then closing the request channels shortly after) that could panic a caller with "send
// on closed channel" if it lost the race — exactly what Mailbox's sendOps-based two-phase shutdown
// (signal, then wait for every in-flight Send to finish, only then close the underlying channel) exists
// to prevent.
type EntityManager struct {
	mailbox  *mailbox.Mailbox
	entities map[string]entityRecord
}

func NewEntityManager() *EntityManager {
	return &EntityManager{
		entities: make(map[string]entityRecord),
	}
}

func (em *EntityManager) Start() error {
	fmt.Printf("EntityManager starting\n")
	em.mailbox = mailbox.NewMailbox()
	return nil
}

func (em *EntityManager) Stop(ctx context.Context) error {
	if em.mailbox == nil {
		return nil
	}

	fmt.Printf("EntityManager stopping\n")

	// Run the stop to completion in a background goroutine regardless of whether the caller's context
	// expires while waiting, so a context timeout only affects whether Stop blocks the caller — it
	// never leaves the mailbox half-stopped. Mailbox.Stop is itself safe to call more than once, so no
	// extra guard is needed here for a repeat Stop call.
	stoppedCh := make(chan struct{})
	go func() {
		em.mailbox.Stop()
		close(stoppedCh)
		fmt.Printf("EntityManager stopped\n")
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("error stopping EntityManager, context done signal received while waiting for termination: %v", ctx.Err())
	case <-stoppedCh:
		return nil
	}
}

func (em *EntityManager) AddEntity(
	id string,
	entityType reflect.Type,
	stubFactoryFn spi.EntityStubFactoryFunc) error {
	addErr, err := em.mailbox.Exec(func() error {
		return em.handleAddEntity(id, entityType, stubFactoryFn)
	})
	if err != nil {
		return errors.New("unable to add, EntityManager is no longer accepting requests")
	}

	return addErr
}

func (em *EntityManager) GetEntity(id string) (EntityRecord, error) {
	response, err := em.mailbox.Exec(func() getEntityResponse {
		return em.handleGetEntity(id)
	})
	if err != nil {
		return nil, errors.New("unable to get, EntityManager is no longer accepting requests")
	}

	return response.entityRecord, response.err
}

func (em *EntityManager) RemoveEntity(registrationId string) error {
	removeErr, err := em.mailbox.Exec(func() error {
		return em.handleRemoveEntity(registrationId)
	})
	if err != nil {
		return errors.New("unable to remove, EntityManager is no longer accepting requests")
	}

	return removeErr
}

func (em *EntityManager) FindEntities(predicate entity.Predicate) ([]EntityRecord, error) {
	response, err := em.mailbox.Exec(func() findEntitiesResponse {
		return em.handleFindEntities(predicate)
	})
	if err != nil {
		return nil, errors.New("unable to find, EntityManager is no longer accepting requests")
	}

	return response.entities, response.err
}

func (em *EntityManager) handleAddEntity(
	id string,
	entityType reflect.Type,
	stubFactoryFn spi.EntityStubFactoryFunc) error {
	_, ok := em.entities[id]

	if ok {
		return fmt.Errorf("an entity with id \"%s\" is already registered", id)
	}

	em.entities[id] = entityRecord{
		id:            id,
		entityType:    entityType,
		stubFactoryFn: stubFactoryFn,
	}

	return nil
}

func (em *EntityManager) handleGetEntity(id string) getEntityResponse {
	e, ok := em.entities[id]

	if !ok {
		return getEntityResponse{
			entityRecord: nil,
			err:          fmt.Errorf("no entity with id \"%s\" is registered", id),
		}
	}

	return getEntityResponse{
		entityRecord: e,
		err:          nil,
	}
}

func (em *EntityManager) handleFindEntities(predicate entity.Predicate) findEntitiesResponse {
	entities := make([]EntityRecord, 0)

	for _, e := range em.entities {
		if predicate == nil {
			entities = append(entities, e)
			continue
		}

		info := entity.RegisteredEntityInfo{
			Id:            e.id,
			EntityType:    e.entityType,
			StubFactoryFn: e.stubFactoryFn,
		}

		if predicate(&info) {
			entities = append(entities, e)
		}
	}

	return findEntitiesResponse{
		entities: entities,
		err:      nil,
	}
}

func (em *EntityManager) handleRemoveEntity(registrationId string) error {
	_, ok := em.entities[registrationId]

	if !ok {
		return fmt.Errorf("no entity with id \"%s\" is registered", registrationId)
	}

	delete(em.entities, registrationId)

	return nil
}
