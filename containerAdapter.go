package core

import (
	"fmt"
	"io/fs"
	"net/http"
	"reflect"

	"github.com/avanha/pmaas-core/internal/plugins"
	"github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/entity"
	"github.com/avanha/pmaas-spi/events"
)

// containerAdapter is an implementation of spi.IPMAASContainer.  It wraps the reference to the
// PMAAS server along with the plugin instance and its config.  This allows us to track the
// plugin calling into the PMAAS server.
type containerAdapter struct {
	pmaas  *PMAAS
	target *plugins.PluginWrapper
}

// Force implementation of IPMAASContainer
var _ spi.IPMAASContainer = (*containerAdapter)(nil)

func (ca *containerAdapter) AddRoute(path string, handlerFunc http.HandlerFunc) {
	registration := plugins.HttpHandlerRegistration{
		Pattern:     path,
		HandlerFunc: handlerFunc,
	}
	ca.target.HttpHandlers = append(ca.target.HttpHandlers, registration)
}

func (ca *containerAdapter) BroadcastEvent(sourceEntityId string, event any) error {
	return ca.pmaas.broadcastEvent(ca.target, sourceEntityId, event)
}

func (ca *containerAdapter) RegisterEntityRenderer(entityType reflect.Type, rendererFactory spi.EntityRendererFactory) {
	registration := plugins.EntityRendererRegistration{
		EntityType:      entityType,
		RendererFactory: rendererFactory,
	}
	ca.target.EntityRenderers = append(ca.target.EntityRenderers, registration)
}

func (ca *containerAdapter) RenderList(w http.ResponseWriter, r *http.Request, options spi.RenderListOptions, items []interface{}) {
	ca.pmaas.renderList(ca.target, w, r, options, items)
}

func (ca *containerAdapter) GetTemplate(templateInfo *spi.TemplateInfo) (spi.CompiledTemplate, error) {
	return ca.pmaas.getTemplate(ca.target, templateInfo)
}

func (ca *containerAdapter) GetEntityRenderer(entityType reflect.Type) (spi.EntityRenderer, error) {
	return ca.pmaas.getEntityRenderer(ca.target, entityType)
}

func (ca *containerAdapter) EnableStaticContent(staticContentDir string) {
	ca.target.StaticContentDir = staticContentDir
}

func (ca *containerAdapter) ProvideContentFS(contentFS fs.FS, prefix string) {
	if prefix == "" {
		ca.target.ContentFS = contentFS
	} else {
		subFS, err := fs.Sub(contentFS, prefix)

		if err != nil {
			panic(fmt.Sprintf("Can't create SubFS instance: %v", err))
		}

		ca.target.ContentFS = subFS
	}
}

func (ca *containerAdapter) RegisterEntity(
	uniqueData string, entityType reflect.Type,
	name string,
	stubFactoryFn spi.EntityStubFactoryFunc) (string, error) {
	return ca.pmaas.registerEntity(ca.target, uniqueData, entityType, name, stubFactoryFn)
}

func (ca *containerAdapter) DeregisterEntity(id string) error {
	return ca.pmaas.deregisterEntity(ca.target, id)
}

func (ca *containerAdapter) RegisterEventReceiver(
	predicate events.EventPredicate,
	receiver events.EventReceiver) (int, error) {
	return ca.pmaas.registerEventReceiver(ca.target, predicate, receiver)
}

func (ca *containerAdapter) DeregisterEventReceiver(handle int) error {
	return ca.pmaas.deregisterEventReceiver(ca.target, handle)
}

func (ca *containerAdapter) ExecOnPluginGoRoutine(f func()) error {
	return ca.target.ExecVoidFn(f)
}

func (ca *containerAdapter) EnqueueOnPluginGoRoutine(f func()) error {
	return ca.target.ExecInternal(f)
}

func (ca *containerAdapter) EnqueueOnServerGoRoutine(f []func()) error {
	return ca.pmaas.enqueueOnServerGoRoutine(f)
}

func (cs *containerAdapter) GetEntities(
	predicate func(info *entity.RegisteredEntityInfo) bool) ([]entity.RegisteredEntityInfo, error) {
	return cs.pmaas.getEntities(predicate)
}

func (ca *containerAdapter) AssertEntityType(pmaasEntityId string, entityType reflect.Type) error {
	return ca.pmaas.assertEntityType(pmaasEntityId, entityType)
}

func (ca *containerAdapter) InvokeOnEntity(entityId string, function func(entity any)) error {
	//return ca.pmaas.invokeOnEntity(entityId, function)
	panic("not implemented")
}

func (ca *containerAdapter) ClosedCallbackChannel() chan func() {
	return ca.pmaas.closedCallbackChannel
}
