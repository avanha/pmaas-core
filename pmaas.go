package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	"pmaas.io/core/config"
	"pmaas.io/core/internal/dispatcher"
	"pmaas.io/core/internal/entitymanager"
	"pmaas.io/core/internal/eventmanager"
	pmaashttp "pmaas.io/core/internal/http"
	"pmaas.io/core/internal/plugins"
	"pmaas.io/core/internal/pmaasserver"
	"pmaas.io/spi"
	"pmaas.io/spi/events"
)

const PMAAS_SERVER_PMAAS_ENTITY_ID = "PMAAS_SERVER"

type PMAAS struct {
	config             *config.Config
	plugins            []*plugins.PluginWrapper
	entityManager      *entitymanager.EntityManager
	eventManager       *eventmanager.EventManager
	dispatcher         *dispatcher.Dispatcher
	selfType           reflect.Type
	pmaasServerAdapter pmaasserver.PmaasServer
}

func NewPMAAS(config *config.Config) *PMAAS {
	instance := &PMAAS{
		config:        config,
		entityManager: entitymanager.NewEntityManager(),
		eventManager:  eventmanager.NewEventManager(),
		dispatcher:    dispatcher.NewDispatcher(),
	}
	instance.selfType = reflect.ValueOf(instance).Elem().Type()
	instance.pmaasServerAdapter = pmaasServerAdapter{pmaas: instance}
	instance.plugins = createPluginWrappers(instance.pmaasServerAdapter, config.Plugins())
	return instance
}

func createPluginWrappers(pmaasServerAdapter pmaasserver.PmaasServer, configuredPlugins []config.PluginWithConfig) []*plugins.PluginWrapper {
	wrappers := make([]*plugins.PluginWrapper, len(configuredPlugins))

	for i, plugin := range configuredPlugins {
		wrappers[i] = plugins.NewPluginWrapper(pmaasServerAdapter, plugin)
	}

	return wrappers
}

func (pmaas *PMAAS) Run() error {
	fmt.Printf("pmaas.Run: Start\n")

	mainCtx, cancelFn := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelFn()

	dispatcherCtx, dispatcherCancelFn := context.WithCancel(context.Background())

	errCh := make(chan error)

	go func() {
		err := pmaas.internalRun(mainCtx)
		dispatcherCancelFn()
		errCh <- err
	}()

	pmaas.dispatcher.Run(dispatcherCtx)

	return <-errCh
}

func (pmaas *PMAAS) internalRun(ctx context.Context) error {
	defer func() {
		for i := len(pmaas.plugins) - 1; i >= 0; i-- {
			stopPluginRunner(pmaas.plugins[i])
		}
	}()

	fmt.Printf("Initializing...\n")

	// Start and initialize each plugin
	for _, plugin := range pmaas.plugins {
		// Start the plugin's plugin runner goroutine and call Init on the plugin
		pmaas.startPluginRunner(plugin)

		// Create a container adapter
		ca := &containerAdapter{
			pmaas:  pmaas,
			target: plugin}

		// Synchronously execute the plugin's Init function via the plugin's plugin runner
		// goroutine, passing the container adapter.
		err := plugin.ExecVoidFn(func() { plugin.Instance.Init(ca) })

		if err != nil {
			panic(errors.New(fmt.Sprintf("%T Init failed: %s\n", ca.target.Instance, err)))
		}
	}

	fmt.Printf("pmaas.Run: Starting core services...\n")
	var err error

	err = pmaas.eventManager.Start()
	if err != nil {
		return err
	}

	err = pmaas.entityManager.Start()
	if err != nil {
		return err
	}

	fmt.Printf("pmaas.Run: Starting plugins...\n")

	startFailures := 0

	// Start plugins
	for _, plugin := range pmaas.plugins {
		err := plugin.ExecVoidFn(func() { plugin.Instance.Start() })

		if err == nil {
			plugin.Running = true
		} else {
			startFailures = startFailures + 1
			fmt.Printf("%T failed to start: %s\n", plugin.Instance, err)
		}
	}

	var httpServer *pmaashttp.HttpServer = nil

	if startFailures == 0 {
		httpServer, err = pmaas.startHttpServer()
		if err == nil {
			// Wait for the done signal
			fmt.Printf("pmaas.Run: Running, waiting for done signal...\n")
			<-ctx.Done()
		} else {
			fmt.Printf("pmaas.Run: HttpServer start failed: %s\n", err)
		}
	}

	if httpServer != nil {
		stopHttpServer(httpServer)
	}

	fmt.Printf("pmaas.Run: Stopping plugins...\n")
	stopPlugins(pmaas.plugins)

	fmt.Printf("pmaas.Run: Stopping core services...\n")
	stopEntityManager(pmaas.entityManager)
	stopEventManager(pmaas.eventManager)

	fmt.Printf("pmaas.Run: End\n")

	return err
}

func (pmaas *PMAAS) startHttpServer() (*pmaashttp.HttpServer, error) {
	httpServer := pmaashttp.NewHttpServer(pmaas.config.HttpPort)
	httpServer.RegisterPluginHandlers(pmaas.plugins)

	return httpServer, httpServer.Start()
}

func stopHttpServer(httpServer *pmaashttp.HttpServer) {
	ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(10*time.Second))
	defer cancelFn()
	err := httpServer.Stop(ctx)

	if err != nil {
		fmt.Printf("Error stopping HttpServer: %v", err)
	}
}

func stopEntityManager(entityManager *entitymanager.EntityManager) {
	ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(10*time.Second))
	defer cancelFn()
	err := entityManager.Stop(ctx)

	if err != nil {
		fmt.Printf("Error stopping EntityManager: %v\n", err)
	}
}

func stopEventManager(eventManager *eventmanager.EventManager) {
	ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(10*time.Second))
	defer cancelFn()
	err := eventManager.Stop(ctx)

	if err != nil {
		fmt.Printf("Error stopping EventManager: %v\n", err)
	}
}

func stopPlugins(plugins []*plugins.PluginWrapper) {
	for i := len(plugins) - 1; i >= 0; i-- {
		plugin := plugins[i]
		startTime := time.Now()
		if plugin.Running {
			plugin2, ok := plugin.Instance.(spi.IPMAASPlugin2)

			if ok {
				stopPlugin2(plugin, plugin2)
			} else {
				stopPlugin(plugin)
			}
		}
		stopPluginRunner(plugin)
		stopDuration := time.Now().Sub(startTime)
		fmt.Printf("PMAAS Stopped %T in %v\n", plugin.Instance, stopDuration)
	}
}

func stopPlugin(plugin *plugins.PluginWrapper) {
	err := plugin.ExecVoidFn(func() { plugin.Instance.Stop() })
	plugin.Running = false
	if err != nil {
		fmt.Printf("%T Stop failed: %s\n", plugin.Instance, err)
	}
}

func stopPlugin2(plugin *plugins.PluginWrapper, instance spi.IPMAASPlugin2) {
	var callbackChannel chan func() = nil
	err := plugin.ExecVoidFn(func() { callbackChannel = instance.StopAsync() })

	if err != nil {
		fmt.Printf("%T Stop failed: %s\n", plugin.Instance, err)
		plugin.Running = false
		return
	}

	doCallbacks := true

	for callback := range callbackChannel {
		if doCallbacks {
			err = plugin.ExecVoidFn(callback)

			if err != nil {
				fmt.Printf("%T Stop callback failed: %s\n", plugin.Instance, err)
				doCallbacks = false
			}
		}
	}

	plugin.Running = false
}

func (pmaas *PMAAS) startPluginRunner(plugin *plugins.PluginWrapper) {
	// Initialize the runner control members
	plugin.ExecRequestCh = make(chan func())
	plugin.ExecRequestChOpen = true
	plugin.ExecRequestChClosed = make(chan bool)
	plugin.RunnerDoneCh = make(chan error)

	// Spin up a goroutine to execute callbacks for the plugin.
	go func() {
		fmt.Printf("%T plugin runner START\n", plugin.Instance)
		// Signal completion before exiting.
		defer func() {
			close(plugin.RunnerDoneCh)
			fmt.Printf("%T plugin runner STOP\n", plugin.Instance)
		}()

		// Just keep executing until the channel closes.
		for f := range plugin.ExecRequestCh {
			f()
		}
	}()

}

func stopPluginRunner(plugin *plugins.PluginWrapper) {
	if plugin.ExecRequestChOpen {
		// Mark that execRequestCh is no longer open, so we don't try to close it twice
		plugin.ExecRequestChOpen = false

		// The solution here is inspired by "multiple senders one receiver" at
		// https://go101.org/article/channel-closing.html

		// Next, signal that execRequestCh channel is about to close
		close(plugin.ExecRequestChClosed)

		// Wait for any pending send operations complete.  They'll either complete the channel write operations,
		// or bail out on the done signal.
		plugin.ExecRequestChSendOps.Wait()

		// Close the channel
		close(plugin.ExecRequestCh)

		// Finally, wait for the runner to indicate completion
		err := <-plugin.RunnerDoneCh
		if err != nil {
			fmt.Printf("%T runner completed with error: %s\n", plugin.Instance, err)
		}
	}
}

func (pmaas *PMAAS) renderList(_ *plugins.PluginWrapper, w http.ResponseWriter, r *http.Request,
	options spi.RenderListOptions, items []interface{}) {
	alt := r.URL.Query()["alt"]

	if len(alt) > 0 && alt[0] == "json" {
		pmaas.renderJsonList(w, r, items)
		return
	}

	var renderPlugin spi.IPMAASRenderPlugin = nil
	for _, plugin := range pmaas.plugins {
		candidate, ok := plugin.Instance.(spi.IPMAASRenderPlugin)

		if ok {
			renderPlugin = candidate
			break
		}
	}

	if renderPlugin == nil {
		panic("No render plugin available")
	}

	renderPlugin.RenderList(w, r, options, items)
}

func (pmaas *PMAAS) renderJsonList(w http.ResponseWriter, _ *http.Request, items []interface{}) {
	b, err := json.MarshalIndent(items, "", "  ")

	if err == nil {
		_, err := w.Write(b)

		if err != nil {
			fmt.Printf("Error writing response: %s\n", err)
		}
	}
}

func (pmaas *PMAAS) getTemplate(
	sourcePlugin *plugins.PluginWrapper, templateInfo *spi.TemplateInfo) (compiledTemplate spi.CompiledTemplate, err error) {
	var templateEnginePlugin spi.IPMAASTemplateEnginePlugin = nil

	for _, plugin := range pmaas.plugins {
		candidate, ok := plugin.Instance.(spi.IPMAASTemplateEnginePlugin)

		if ok {
			templateEnginePlugin = candidate
			break
		}
	}

	if templateEnginePlugin == nil {
		panic("No instance IPMAASTemplateEnginePlugin available")
	}

	contentFS, _ := sourcePlugin.ContentFs()

	if contentFS == nil {
		panic(fmt.Sprintf("No fs.FS implementation available for plugin %s", sourcePlugin.PluginPath()))
	}

	webPath := "/" + sourcePlugin.PluginPath()
	updatedScripts := make([]string, len(templateInfo.Scripts))
	updatedStyles := make([]string, len(templateInfo.Styles))

	for i, script := range templateInfo.Scripts {
		updatedScripts[i] = webPath + "/" + script
	}

	for i, style := range templateInfo.Styles {
		updatedStyles[i] = webPath + "/" + style
	}

	updatedTemplateInfo := spi.TemplateInfo{
		Name:     templateInfo.Name,
		FuncMap:  templateInfo.FuncMap,
		Paths:    templateInfo.Paths,
		Scripts:  updatedScripts,
		Styles:   updatedStyles,
		SourceFS: contentFS,
	}

	defer func() {
		if r := recover(); r != nil {
			compiledTemplate = spi.CompiledTemplate{}
			err = errors.New(fmt.Sprintf("Panic in TemplateEnginePlugin.GetTemplate(): %v", r))
		}
	}()

	return templateEnginePlugin.GetTemplate(&updatedTemplateInfo)
}

func (pmaas *PMAAS) getEntityRenderer(_ *plugins.PluginWrapper, entityType reflect.Type) (spi.EntityRenderer, error) {
	var rendererFactory spi.EntityRendererFactory

	for _, plugin := range pmaas.plugins {
		for _, entityRendererRegistration := range plugin.EntityRenderers {
			if entityType.AssignableTo(entityRendererRegistration.EntityType) {
				rendererFactory = entityRendererRegistration.RendererFactory
			}
		}
	}

	// Did we find anything?
	if rendererFactory == nil {
		// No, return a generic renderer
		return spi.EntityRenderer{RenderFunc: genericEntityRenderer}, nil
	}

	// Use the factory we found
	renderer, err := rendererFactory()

	if err != nil {
		return spi.EntityRenderer{}, fmt.Errorf("rendererFactory failed: %w", err)
	}

	if renderer.RenderFunc != nil {
		return renderer, nil
	}

	if renderer.StreamingRenderFunc != nil {
		wrapperFunc := func(entity any) (string, error) {
			var buffer bytes.Buffer
			err := renderer.StreamingRenderFunc(&buffer, entity)

			if err != nil {
				return "", fmt.Errorf("error executing StreamingEntityRenderFunc: %v", err)
			}

			return buffer.String(), nil
		}

		return spi.EntityRenderer{
				RenderFunc:          wrapperFunc,
				StreamingRenderFunc: renderer.StreamingRenderFunc,
				Styles:              renderer.Styles,
				Scripts:             renderer.Scripts},
			nil
	}

	return spi.EntityRenderer{},
		fmt.Errorf("invalid EntityRenderer instance, both RenderFunc and StreamingRenderFunc are nil")
}

func (pmaas *PMAAS) registerEntity(
	sourcePlugin *plugins.PluginWrapper,
	uniqueData string,
	entityType reflect.Type,
	name string,
	invocationHandlerFn spi.EntityInvocationHandlerFunc) (string, error) {
	id := fmt.Sprintf("%s_%s_%s", sourcePlugin.PluginType.PkgPath(), sourcePlugin.PluginType.Name(), uniqueData)
	id = strings.ReplaceAll(id, " ", "_")
	err := pmaas.entityManager.AddEntity(id, entityType, invocationHandlerFn)

	if err != nil {
		return "", err
	}

	event := events.EntityRegisteredEvent{EntityEvent: events.EntityEvent{Id: id, EntityType: entityType, Name: name}}
	err = pmaas.eventManager.BroadcastEvent(pmaas.selfType, PMAAS_SERVER_PMAAS_ENTITY_ID, event)

	if err != nil {
		fmt.Printf("Unable to broadcast %s: %v", event, err)
	}

	return id, nil
}

func (pmaas *PMAAS) deregisterEntity(_ *plugins.PluginWrapper, id string) error {
	entityRecord, err := pmaas.entityManager.GetEntity(id)

	if err != nil {
		return fmt.Errorf("deregisterEntity failed, unable to get entity %s: %v", id, err)
	}

	err = pmaas.entityManager.RemoveEntity(id)

	if err != nil {
		return fmt.Errorf("deregisterEntity failed, unable to remove entity %s: %v", id, err)
	}

	event := events.EntityDeregisteredEvent{EntityEvent: events.EntityEvent{Id: id, EntityType: entityRecord.GetEntityType()}}
	err = pmaas.eventManager.BroadcastEvent(pmaas.selfType, PMAAS_SERVER_PMAAS_ENTITY_ID, event)

	if err != nil {
		fmt.Printf("Unable to broadcast %s: %v", event, err)
	}

	return nil
}

func (pmaas *PMAAS) broadcastEvent(sourcePlugin *plugins.PluginWrapper, sourceEntityId string, event any) error {
	return pmaas.eventManager.BroadcastEvent(sourcePlugin.PluginType, sourceEntityId, event)
}

func (pmaas *PMAAS) registerEventReceiver(
	sourcePlugin *plugins.PluginWrapper,
	predicate events.EventPredicate,
	receiver events.EventReceiver) (int, error) {
	return pmaas.eventManager.AddReceiver(sourcePlugin, predicate, receiver)
}

func (pmaas *PMAAS) deregisterEventReceiver(
	_ *plugins.PluginWrapper, handle int) error {
	return pmaas.eventManager.RemoveReceiver(handle)
}

func (pmaas *PMAAS) assertEntityType(entityId string, entityType reflect.Type) error {
	entityRegistration, err := pmaas.entityManager.GetEntity(entityId)

	if err != nil {
		return fmt.Errorf("assertEntityType failed, unable to get entity %s: %v", entityId, err)
	}

	actualEntityType := entityRegistration.GetEntityType()
	if !actualEntityType.AssignableTo(entityType) {
		return fmt.Errorf("assertEntityType failed, entity %s is a %s not a %s",
			entityId, actualEntityType, entityType)
	}

	return nil
}

func (pmaas *PMAAS) invokeOnEntity(entityId string, function func(entity any)) error {
	entityRegistration, err := pmaas.entityManager.GetEntity(entityId)

	if err != nil {
		return fmt.Errorf("invokeOnEntity failed, unable to get entity %s: %v", entityId, err)
	}

	invocationHandler := entityRegistration.GetInvocationHandler()

	if invocationHandler == nil {
		return fmt.Errorf("invokeOnEntity failed, invocation handler for entity %s is nil", entityId)
	}

	err = invocationHandler(function)

	if err != nil {
		return fmt.Errorf(
			"invokeOnEntity %s failed, plugin invocationHandler returned an error: %v",
			entityId, err)
	}

	return nil
}

func (pmaas *PMAAS) enqueueOnServerGoRoutine(callbacks []func()) error {
	return pmaas.dispatcher.Dispatch(callbacks)
}

func genericEntityRenderer(entity any) (string, error) {
	return fmt.Sprintf("<div>%T</div>", entity), nil
}
