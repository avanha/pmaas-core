package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/signal"
	"reflect"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/avanha/pmaas-core/config"
	"github.com/avanha/pmaas-core/internal/configstore"
	"github.com/avanha/pmaas-core/internal/dispatcher"
	"github.com/avanha/pmaas-core/internal/entitymanager"
	"github.com/avanha/pmaas-core/internal/eventmanager"
	pmaashttp "github.com/avanha/pmaas-core/internal/http"
	"github.com/avanha/pmaas-core/internal/plugins"
	"github.com/avanha/pmaas-core/internal/pmaasserver"
	"github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/entity"
	"github.com/avanha/pmaas-spi/events"
)

const PmaasServerPmaasEntityId = "PMAAS_SERVER"

type PMAAS struct {
	config                *config.Config
	plugins               []*plugins.PluginWrapper
	entityManager         *entitymanager.EntityManager
	eventManager          *eventmanager.EventManager
	dispatcher            *dispatcher.Dispatcher
	selfType              reflect.Type
	pmaasServerAdapter    pmaasserver.PmaasServer
	closedCallbackChannel chan func()
	configStore           *configstore.ConfigStore

	// menuEntries/menuEntriesByShortName hold the navigation menu built from every plugin's
	// menu-visible routes (see registerMenuRoute). Both are populated only while plugins are
	// being initialized (Init runs synchronously, plugin by plugin, before the HTTP server ever
	// starts accepting requests) and are never modified afterward, so getMenu can be called from
	// any goroutine without locking.
	menuEntries            []*spi.MenuEntry
	menuEntriesByShortName map[string]*spi.MenuEntry

	// tlsCertificateProvider is set by at most one plugin's ProvideTLSCertificate call, during Init
	// or Start - i.e. before startHttpServer ever reads it, since that only happens after every
	// plugin has finished starting (see internalRun). Like menuEntries, it's only ever written while
	// plugins are being initialized/started sequentially, so no locking is needed here.
	tlsCertificateProvider func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	// rootStatusHandler is set by at most one plugin's ProvideRootStatusHandler call, during
	// Init or Start, under the same timing/locking reasoning as tlsCertificateProvider above.
	rootStatusHandler spi.RootStatusHandlerFunc

	// startTime/pluginVersions/assemblyName/assemblyVersion/assemblyCommitTime back
	// getServerStatus's ServerStatus.Uptime/Plugins/AssemblyName/AssemblyVersion/CommitTime.
	// startTime is set once, in NewPMAAS, and the rest are resolved once, right after
	// instance.plugins is populated - none of them ever change afterward, so getServerStatus
	// can read them from any goroutine without locking.
	startTime          time.Time
	pluginVersions     []spi.PluginVersion
	assemblyName       string
	assemblyVersion    string
	assemblyCommitTime time.Time
}

func NewPMAAS(config *config.Config) *PMAAS {
	instance := &PMAAS{
		config:        config,
		entityManager: entitymanager.NewEntityManager(),
		eventManager:  eventmanager.NewEventManager(),
		dispatcher:    dispatcher.NewDispatcher(),
		configStore:   configstore.NewConfigStore(),
		startTime:     time.Now(),
	}
	instance.selfType = reflect.ValueOf(instance).Elem().Type()
	instance.pmaasServerAdapter = pmaasServerAdapter{pmaas: instance}
	instance.plugins = createPluginWrappers(instance.pmaasServerAdapter, config.Plugins())
	instance.pluginVersions = collectPluginVersions(instance.plugins)
	buildInfo, _ := debug.ReadBuildInfo()
	instance.assemblyName, instance.assemblyVersion = assemblyInfo(buildInfo)
	instance.assemblyCommitTime = commitTime(buildInfo)

	// Create a channel and close it right away.  Plugins can use this to avoid the repetition and overhead of
	// creating and closing a channel.
	instance.closedCallbackChannel = make(chan func())
	close(instance.closedCallbackChannel)
	instance.menuEntriesByShortName = make(map[string]*spi.MenuEntry)

	return instance
}

// registerMenuRoute records fullPath's navigation-menu visibility for pluginShortName, applying
// HttpHandlerOptions' defaults: a plugin's list route (relativePath "") defaults to included, every
// other route defaults to excluded - either can be overridden via options.IncludeInMenu. Only one
// level of nesting is supported: every included non-list-route becomes a flat child of its plugin's
// single menu entry, regardless of how many path segments its relativePath has. Called only while
// plugins are being initialized (see containerAdapter.AddRouteWithOptions), before the HTTP server
// starts, so no synchronization is needed here or in getMenu.
func (pmaas *PMAAS) registerMenuRoute(
	pluginShortName string, fullPath string, relativePath string, options *spi.HttpHandlerOptions) {
	isListRoute := relativePath == ""

	include := isListRoute
	if options.IncludeInMenu != nil {
		include = *options.IncludeInMenu
	}

	if !include {
		return
	}

	label := options.MenuLabel
	if label == "" {
		if isListRoute {
			label = pluginShortName
		} else {
			label = relativePath
		}
	}

	entry := pmaas.menuEntriesByShortName[pluginShortName]

	if entry == nil {
		entry = &spi.MenuEntry{}
		pmaas.menuEntriesByShortName[pluginShortName] = entry
		pmaas.menuEntries = append(pmaas.menuEntries, entry)
	}

	if isListRoute {
		entry.Path = fullPath
		entry.Label = label
		entry.Icon = options.MenuIcon
	} else {
		entry.Children = append(entry.Children, spi.MenuEntry{Path: fullPath, Label: label, Icon: options.MenuIcon})
	}
}

// getMenu returns a snapshot of the navigation menu. Each top-level entry is copied out of the
// pmaas.menuEntries pointers so the caller can't mutate this PMAAS instance's own state through it.
func (pmaas *PMAAS) getMenu() []spi.MenuEntry {
	result := make([]spi.MenuEntry, len(pmaas.menuEntries))

	for i, entry := range pmaas.menuEntries {
		result[i] = *entry
	}

	return result
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
			pmaas.plugins[i].StopPluginRunner()
		}
	}()

	fmt.Printf("Initializing...\n")

	// Start and initialize each plugin
	for _, plugin := range pmaas.plugins {
		// Start the plugin's plugin runner goroutine and call Init on the plugin
		plugin.StartPluginRunner()

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

// getBaseUrl picks the configured base URL matching the given request's Host header. The scheme and
// host are always taken from configuration, never from the request itself: a request only selects
// which pre-configured base URL applies, it never supplies the value directly.
func (pmaas *PMAAS) getBaseUrl(r *http.Request) (string, error) {
	for _, baseUrl := range pmaas.config.BaseURLs {
		parsed, err := url.Parse(baseUrl)

		if err != nil {
			fmt.Printf("pmaas.getBaseUrl: ignoring invalid configured base URL %q: %v\n", baseUrl, err)
			continue
		}

		if parsed.Host == r.Host {
			return baseUrl, nil
		}
	}

	return "", fmt.Errorf("no configured base URL matches request host %q", r.Host)
}

func (pmaas *PMAAS) startHttpServer() (*pmaashttp.HttpServer, error) {
	httpServer := pmaashttp.NewHttpServer(pmaas.config.HttpPort)
	httpServer.RegisterPluginHandlers(pmaas.plugins)

	if pmaas.tlsCertificateProvider != nil {
		httpServer.SetTLSCertificateProvider(pmaas.tlsCertificateProvider)
	}

	if pmaas.rootStatusHandler != nil {
		httpServer.SetRootHandler(pmaas.handleRootStatusRequest)
	}

	return httpServer, httpServer.Start()
}

// handleRootStatusRequest is only ever wired in as the server's root handler when
// rootStatusHandler is non-nil (see startHttpServer), so it never needs to check that itself.
func (pmaas *PMAAS) handleRootStatusRequest(w http.ResponseWriter, r *http.Request) {
	pmaas.rootStatusHandler(w, r, pmaas.getServerStatus())
}

// getServerStatus computes a fresh ServerStatus snapshot for the root status page - see
// IPMAASContainer.ProvideRootStatusHandler. Safe to call from any goroutine: pluginVersions and
// startTime never change after NewPMAAS, and readLoadAverage/readMemoryStats each read live
// process/system state independently, with no shared state of their own to guard.
func (pmaas *PMAAS) getServerStatus() spi.ServerStatus {
	return spi.ServerStatus{
		Uptime:          time.Since(pmaas.startTime),
		AssemblyName:    pmaas.assemblyName,
		AssemblyVersion: pmaas.assemblyVersion,
		CommitTime:      pmaas.assemblyCommitTime,
		Plugins:         pmaas.pluginVersions,
		LoadAverage:     readLoadAverage(),
		Memory:          readMemoryStats(),
	}
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
			stopPlugin(plugin)
		}
		plugin.StopPluginRunner()
		stopDuration := time.Now().Sub(startTime)
		fmt.Printf("PMAAS Stopped %T in %v\n", plugin.Instance, stopDuration)
	}
}

func stopPlugin(plugin *plugins.PluginWrapper) {
	var callbackChannel chan func() = nil
	err := plugin.ExecVoidFn(func() { callbackChannel = plugin.Instance.Stop() })

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

	updatedScripts := make([]string, len(templateInfo.Scripts))
	updatedStyles := make([]string, len(templateInfo.Styles))

	for i, script := range templateInfo.Scripts {
		updatedScripts[i] = sourcePlugin.AssetFullPath(script)
	}

	for i, style := range templateInfo.Styles {
		updatedStyles[i] = sourcePlugin.AssetFullPath(style)
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

type stubFactoryResult struct {
	stub any
	err  error
}

func (pmaas *PMAAS) registerEntity(
	sourcePlugin *plugins.PluginWrapper,
	uniqueData string,
	entityType reflect.Type,
	name string,
	stubFactoryFn spi.EntityStubFactoryFunc) (string, error) {
	id := fmt.Sprintf("%s_%s_%s", sourcePlugin.PluginType.PkgPath(), sourcePlugin.PluginType.Name(), uniqueData)
	id = strings.ReplaceAll(id, " ", "_")

	// A nil stubFactoryFn means the entity has no stub; keep the wrapper nil too, so consumers'
	// "StubFactoryFn != nil" checks work instead of hitting a nil call on the plugin goroutine.
	var wrappedStubFactory spi.EntityStubFactoryFunc

	if stubFactoryFn != nil {
		wrappedStubFactory = func() (any, error) {
			resultCh := make(chan stubFactoryResult)
			err := sourcePlugin.ExecInternal(func() {
				stub, factoryErr := stubFactoryFn()
				resultCh <- stubFactoryResult{stub: stub, err: factoryErr}
				close(resultCh)
			})

			if err != nil {
				return nil, fmt.Errorf("stub creation failed, unable to execute stubFactory on plugin goroutine: %v", err)
			}

			result := <-resultCh

			return result.stub, result.err
		}
	}

	err := pmaas.entityManager.AddEntity(id, entityType, wrappedStubFactory)

	if err != nil {
		return "", err
	}

	event := events.EntityRegisteredEvent{EntityEvent: events.EntityEvent{Id: id, EntityType: entityType, Name: name}, StubFactoryFn: wrappedStubFactory}
	err = pmaas.eventManager.BroadcastEvent(pmaas.selfType, PmaasServerPmaasEntityId, event)

	if err != nil {
		fmt.Printf("Unable to broadcast %v: %v", event, err)
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
	err = pmaas.eventManager.BroadcastEvent(pmaas.selfType, PmaasServerPmaasEntityId, event)

	if err != nil {
		fmt.Printf("Unable to broadcast %v: %v", event, err)
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

func (pmaas *PMAAS) getEntities(
	predicate func(info *entity.RegisteredEntityInfo) bool) ([]entity.RegisteredEntityInfo, error) {
	registrations, err := pmaas.entityManager.FindEntities(predicate)

	if err != nil {
		return nil, fmt.Errorf("getEntities failed: %w", err)
	}

	result := make([]entity.RegisteredEntityInfo, len(registrations))

	for i, registration := range registrations {
		result[i] = entity.RegisteredEntityInfo{
			Id:            registration.GetId(),
			EntityType:    registration.GetEntityType(),
			Name:          "Name_" + registration.GetId(),
			StubFactoryFn: registration.GetStubFactoryFn(),
		}
	}

	return result, nil
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

/*
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
*/

func (pmaas *PMAAS) loadConfig(pluginType reflect.Type, targetFactoryFunc func(string) any) (any, error) {
	return pmaas.configStore.Load(pluginType, targetFactoryFunc)
}

func (pmaas *PMAAS) saveConfig(pluginType reflect.Type, config any) error {
	return pmaas.configStore.Save(pluginType, config)
}

func (pmaas *PMAAS) enqueueOnServerGoRoutine(callbacks []func()) error {
	return pmaas.dispatcher.Dispatch(callbacks)
}

func (pmaas *PMAAS) provideTLSCertificate(
	getCertificateFunc func(*tls.ClientHelloInfo) (*tls.Certificate, error)) error {
	if pmaas.tlsCertificateProvider != nil {
		return errors.New("a TLS certificate provider has already been registered by another plugin")
	}

	pmaas.tlsCertificateProvider = getCertificateFunc

	return nil
}

func (pmaas *PMAAS) provideRootStatusHandler(handlerFunc spi.RootStatusHandlerFunc) error {
	if pmaas.rootStatusHandler != nil {
		return errors.New("a root status handler has already been registered by another plugin")
	}

	pmaas.rootStatusHandler = handlerFunc

	// Prepended, not appended, so the status page reliably ends up first in the nav menu - the
	// natural "home" position - regardless of which plugin happens to register it, or when
	// during plugin initialization that happens relative to every other plugin's own routes.
	pmaas.menuEntries = append([]*spi.MenuEntry{{Path: "/", Label: "Status"}}, pmaas.menuEntries...)

	return nil
}

func genericEntityRenderer(entity any) (string, error) {
	return fmt.Sprintf("<div>%T</div>", entity), nil
}
