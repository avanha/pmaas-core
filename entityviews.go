package core

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/avanha/pmaas-spi"
)

// entityOwner is the plugin that registered an entity: what the server needs of it to show the entity. *plugins.PluginWrapper is one.
type entityOwner interface {
	ShortName() string
	RouteFullPath(relativePath string) string

	// ExecVoidFn runs the function on the plugin's goroutine and waits for it.
	ExecVoidFn(target func()) error
}

// entityInfo is what the server keeps of an entity besides what the entity manager does.
type entityInfo struct {
	name       string
	entityType reflect.Type
	owner      entityOwner

	// view is how the entity looks on a page, if its plugin said.
	view spi.EntityViewFunc
}

func (pmaas *PMAAS) setEntityInfo(id string, info *entityInfo) {
	pmaas.entityInfoMu.Lock()
	defer pmaas.entityInfoMu.Unlock()

	pmaas.entityInfo[id] = info
}

func (pmaas *PMAAS) removeEntityInfo(id string) {
	pmaas.entityInfoMu.Lock()
	defer pmaas.entityInfoMu.Unlock()

	delete(pmaas.entityInfo, id)
}

// entityName is the name the entity was registered with, or its id if it isn't known.
func (pmaas *PMAAS) entityName(id string) string {
	pmaas.entityInfoMu.RLock()
	defer pmaas.entityInfoMu.RUnlock()

	if info, ok := pmaas.entityInfo[id]; ok {
		return info.name
	}

	return id
}

// registerEntityView gives an entity a view. Only the plugin that registered the entity can.
func (pmaas *PMAAS) registerEntityView(source entityOwner, id string, view spi.EntityViewFunc) error {
	if view == nil {
		return errors.New("registerEntityView failed, the view function is nil")
	}

	pmaas.entityInfoMu.Lock()
	defer pmaas.entityInfoMu.Unlock()

	info, ok := pmaas.entityInfo[id]
	if !ok {
		return fmt.Errorf("registerEntityView failed, no entity %s is registered", id)
	}

	if info.owner != source {
		return fmt.Errorf("registerEntityView failed, entity %s was registered by another plugin", id)
	}

	info.view = view

	return nil
}

// getEntityViews makes the views of the entities, one after another, each on its plugin's goroutine.
func (pmaas *PMAAS) getEntityViews(ids []string) []spi.EntityViewResult {
	results := make([]spi.EntityViewResult, len(ids))

	for i, id := range ids {
		results[i] = pmaas.getEntityView(id)
	}

	return results
}

func (pmaas *PMAAS) getEntityView(id string) spi.EntityViewResult {
	pmaas.entityInfoMu.RLock()
	info, ok := pmaas.entityInfo[id]

	var view spi.EntityViewFunc
	if ok {
		view = info.view
	}

	pmaas.entityInfoMu.RUnlock()

	if !ok {
		return spi.EntityViewResult{EntityId: id, Name: id, Err: fmt.Errorf("no entity %s is registered", id)}
	}

	result := spi.EntityViewResult{
		EntityId: id, Name: info.name, EntityType: info.entityType,
		Plugin: info.owner.ShortName(), PluginPath: info.owner.RouteFullPath(""),
	}

	if view == nil {
		return result
	}

	var made spi.EntityView

	var viewErr error

	if err := info.owner.ExecVoidFn(func() { made, viewErr = callView(view) }); err != nil {
		result.Err = fmt.Errorf("unable to run the view of %s on its plugin: %w", id, err)

		return result
	}

	if viewErr != nil {
		result.Err = viewErr

		return result
	}

	result.View = &made

	return result
}

// callView runs a plugin's view function, and takes a panic in it for an error: the plugin's goroutine is not ours to lose.
func callView(view spi.EntityViewFunc) (made spi.EntityView, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the view panicked: %v", r)
		}
	}()

	return view()
}
