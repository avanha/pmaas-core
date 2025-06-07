package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"pmaas.io/spi"
	"pmaas.io/spi/events"
)

type PluginConfig struct {
	ContentPathOverride string
	StaticContentDir    string `default:"static"`
}

type httpHandlerRegistration struct {
	pattern     string
	handlerFunc http.HandlerFunc
}

type entityRendererRegistration struct {
	entityType      reflect.Type
	rendererFactory spi.EntityRendererFactory
}

type pluginWithConfig struct {
	config           *PluginConfig
	instance         spi.IPMAASPlugin
	httpHandlers     []*httpHandlerRegistration
	entityRenderers  []entityRendererRegistration
	staticContentDir string
	contentFS        fs.FS
	pluginType       reflect.Type
	running          bool

	// An unbuffered channel for work to execute on the plugin runner goroutine
	execRequestCh chan func()

	// A channel that is closed when the plugin runner goroutine needs to stop, and doesn't want any further writes to
	// execRequestCh.  This is checked by the sender.
	execRequestChClosed chan bool

	// A boolean that tracks whether execRequestCh is open.  This is needed because the main pmaas Run function calls
	// stopPluginRunner multiple times. Under normal execution, when the plugin is stopped, but there's also a fallback
	// deferred execution fom the main Run function.
	execRequestChOpen bool

	// A WaitGroup that counts the number of senders currently trying to write to execRequestCh. The stopPluginRunner
	// function waits for this to be zero before actually closing execRequestCh.  This ensures that no writes to a closed
	// channel can take place.  All senders will have either completed their write, or detected that a stop is in
	// progress, via execRequestChClosed.
	execRequestChSendOps sync.WaitGroup

	// A channel that is closed when the plugin runner goroutine is about to complete.
	runnerDoneCh chan error
}

// Executes a function that returns an error using the plugin's plugin runner goroutine.  Returns
// after execution completes, or early, if there was a problem enqueueing.
func (pwc *pluginWithConfig) execErrorFn(target func() error) error {
	errCh := make(chan error)
	f := func() {
		defer func() { close(errCh) }()
		errCh <- target()
	}

	err := pwc.execInternal(f)

	if err != nil {
		return err
	}

	return <-errCh
}

// Executes a function that doesn't return anything on the plugin's plugin runner goroutine.
// Returns after execution completes, or early, if there was a problem enqueueing.
func (pwc *pluginWithConfig) execVoidFn(target func()) error {
	doneCh := make(chan bool)
	f := func() {
		defer func() { close(doneCh) }()
		target()
	}

	err := pwc.execInternal(f)

	if err != nil {
		return err
	}

	<-doneCh
	return nil
}

// Enqueues the specified function to execute on the plugin's runner thread.  Returns an error
// if the function cannot be enqueued, for example when the runner has stopped accepting requests
// as part of the shutdown process.
func (pwc *pluginWithConfig) execInternal(target func()) error {
	var err error = nil

	// The solution here is inspired by "multiple senders one receiver" at
	// https://go101.org/article/channel-closing.html

	// Indicate that a send attempt is in progress to avoid having the channel closed while it's
	// used in the select statement.
	pwc.execRequestChSendOps.Add(1)
	defer pwc.execRequestChSendOps.Done()

	// Check execRequestChClosed channel to avoid doing any extra work.
	select {
	case <-pwc.execRequestChClosed:
		// This means the read operation completed immediately, because either there was a value,
		// or the channel is closed.  We don't care about the value, the channel is never written
		// to, closed.
		err = errors.New("unable to execute target function, execRequestCh is closed")
		break
	default:
		// There was no value, nor immediate return, which means the channel is probably open
		break
	}

	if err != nil {
		return err
	}

	//fmt.Printf("Attempting to send to execRequestCh\n")

	// Either enqueue the callback or return an error if execRequestChClosed.  That should
	// not happen, since closing is guarded by the execRequestChSendOps WaitGroup, but this
	// will defensively avoid a lockup or send on a closed channel.
	select {
	case <-pwc.execRequestChClosed:
		err = errors.New("unable to execute target function, execRequestCh is closed")
		break
	case pwc.execRequestCh <- target:
		break
	}

	//fmt.Printf("Completed send to execRequestCh\n")

	return err
}

type Config struct {
	ContentPathRoot string
	HttpPort        int
	plugins         []*pluginWithConfig
}

type dirWithLogger struct {
	delegate http.FileSystem
}

func (dwl dirWithLogger) Open(name string) (http.File, error) {
	file, err := dwl.delegate.Open(name)

	if err != nil {
		fmt.Printf("Error opening %s: %v\n", name, err)
	}

	return file, err
}

func NewConfig() *Config {
	return &Config{
		ContentPathRoot: "/var/pmaas/content",
		HttpPort:        8090,
		plugins:         nil,
	}
}

func (c *Config) AddPlugin(plugin spi.IPMAASPlugin, config PluginConfig) {
	var wrapper = &pluginWithConfig{
		config:               &config,
		instance:             plugin,
		httpHandlers:         make([]*httpHandlerRegistration, 0),
		entityRenderers:      make([]entityRendererRegistration, 0),
		pluginType:           getPluginType(plugin),
		execRequestCh:        nil,
		execRequestChOpen:    false,
		execRequestChClosed:  nil,
		execRequestChSendOps: sync.WaitGroup{},
		runnerDoneCh:         nil,
		running:              false,
	}

	if c.plugins == nil {
		c.plugins = []*pluginWithConfig{wrapper}
	} else {
		c.plugins = append(c.plugins, wrapper)
	}
}

func getPluginType(plugin spi.IPMAASPlugin) reflect.Type {
	pluginType := reflect.TypeOf(plugin)

	if pluginType.Kind() == reflect.Ptr {
		pluginType = reflect.ValueOf(plugin).Elem().Type()
	}

	return pluginType
}

// containerAdapter is an implementation of spi.IPMAASContainer.  It wraps the reference to the
// PMAAS server along with the plugin instance and its config.  This allows us to track the
// plugin calling into the PMAAS server.
type containerAdapter struct {
	pmaas  *PMAAS
	target *pluginWithConfig
}

// Force implementation of IPMAASContainer
var _ spi.IPMAASContainer = (*containerAdapter)(nil)

func (ca *containerAdapter) AddRoute(path string, handlerFunc http.HandlerFunc) {
	registration := httpHandlerRegistration{
		pattern:     path,
		handlerFunc: handlerFunc,
	}
	ca.target.httpHandlers = append(ca.target.httpHandlers, &registration)
}

func (ca *containerAdapter) BroadcastEvent(event any) error {
	return ca.pmaas.broadcastEvent(ca.target, event)
}

func (ca *containerAdapter) RegisterEntityRenderer(entityType reflect.Type, rendererFactory spi.EntityRendererFactory) {
	registration := entityRendererRegistration{
		entityType:      entityType,
		rendererFactory: rendererFactory,
	}
	ca.target.entityRenderers = append(ca.target.entityRenderers, registration)
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
	ca.target.staticContentDir = staticContentDir
}

func (ca *containerAdapter) ProvideContentFS(contentFS fs.FS, prefix string) {
	if prefix == "" {
		ca.target.contentFS = contentFS
	} else {
		subFS, err := fs.Sub(contentFS, prefix)

		if err != nil {
			panic(fmt.Sprintf("Can't create SubFS instance: %v", err))
		}

		ca.target.contentFS = subFS
	}
}

func (ca *containerAdapter) RegisterEntity(uniqueData string, entityType reflect.Type, name string) (string, error) {
	return ca.pmaas.registerEntity(ca.target, uniqueData, entityType, name)
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
	return ca.target.execVoidFn(f)
}

func (ca *containerAdapter) EnqueueOnPluginGoRoutine(f func()) error {
	return ca.target.execInternal(f)
}

type PMAAS struct {
	config        *Config
	plugins       []*pluginWithConfig
	entityManager *EntityManager
	eventManager  *EventManager
	selfType      reflect.Type
}

func NewPMAAS(config *Config) *PMAAS {
	instance := &PMAAS{
		config:        config,
		plugins:       config.plugins,
		entityManager: NewEntityManager(),
		eventManager:  NewEventManager(),
	}

	instance.selfType = reflect.ValueOf(instance).Elem().Type()
	return instance
}

func hello(w http.ResponseWriter, _ *http.Request) {
	_, err := io.WriteString(w, "Hello!\n")
	if err != nil {
		fmt.Printf("Error writing resposne: %v\n", err)
	}
}

func (pmaas *PMAAS) Run() error {
	fmt.Printf("pmaas.Run: Start\n")

	mainCtx, cancelFn := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelFn()
	defer func() {
		for i := len(pmaas.plugins) - 1; i >= 0; i-- {
			stopPluginRunner(pmaas.plugins[i])
		}
	}()

	fmt.Printf("Initializing...\n")

	// Start an initialize each plugin
	for _, plugin := range pmaas.plugins {
		// Start the plugin's plugin runner goroutine, and call Init on the plugin
		pmaas.startPluginRunner(plugin)

		// Create a container adapter
		ca := &containerAdapter{
			pmaas:  pmaas,
			target: plugin}

		// Synchronously execute the plugin's Init function via the plugin's plugin runner
		// goroutine, passing it the container adapter.
		err := plugin.execVoidFn(func() { plugin.instance.Init(ca) })

		if err != nil {
			panic(errors.New(fmt.Sprintf("%T Init failed: %s\n", ca.target.instance, err)))
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
		err := plugin.execVoidFn(func() { plugin.instance.Start() })
		if err == nil {
			plugin.running = true
		} else {
			startFailures = startFailures + 1
			fmt.Printf("%T failed to start: %s\n", plugin.instance, err)
		}
	}

	var httpServer *HttpServer = nil

	if startFailures == 0 {
		httpServer, err = pmaas.startHttpServer()
		if err == nil {
			// Wait for the done signal
			fmt.Printf("pmaas.Run: Running, waiting for done sginal...\n")
			<-mainCtx.Done()
			fmt.Printf("pmaas.Run: Done sginal recevied, stopping...\n")
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

	return nil
}

func (pmaas *PMAAS) startHttpServer() (*HttpServer, error) {
	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/hello", hello)
	//serveMux.HandleFunc("/plugin", listPlugins)

	for _, plugin := range pmaas.plugins {
		fmt.Printf("Plugin %T config: %v\n", plugin.instance, plugin.config)
		if plugin.staticContentDir != "" {
			pmaas.configurePluginStaticContentDir(plugin, serveMux)
		}

		for _, httpRegistration := range plugin.httpHandlers {
			serveMux.HandleFunc(httpRegistration.pattern, httpRegistration.handlerFunc)
		}
	}

	fmt.Printf("serverMux: %v\n", serveMux)

	httpServer := NewHttpServer(serveMux, pmaas.config.HttpPort)

	return httpServer, httpServer.Start()
}

func stopHttpServer(httpServer *HttpServer) {
	ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(10*time.Second))
	defer cancelFn()
	err := httpServer.Stop(ctx)

	if err != nil {
		fmt.Printf("Error stopping HttpServer: %v", err)
	}
}

func stopEntityManager(entityManager *EntityManager) {
	ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(10*time.Second))
	defer cancelFn()
	err := entityManager.Stop(ctx)

	if err != nil {
		fmt.Printf("Error stopping EntityManager: %v\n", err)
	}
}

func stopEventManager(eventManager *EventManager) {
	ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(10*time.Second))
	defer cancelFn()
	err := eventManager.Stop(ctx)

	if err != nil {
		fmt.Printf("Error stopping EventManager: %v\n", err)
	}
}

func stopPlugins(plugins []*pluginWithConfig) {
	for i := len(plugins) - 1; i >= 0; i-- {
		plugin := plugins[i]
		if plugin.running {
			err := plugin.execVoidFn(func() { plugin.instance.Stop() })
			plugin.running = false
			if err != nil {
				fmt.Printf("%T Stop failed: %s\n", plugin.instance, err)
			}
		}
		stopPluginRunner(plugin)
	}
}

func (pmaas *PMAAS) startPluginRunner(plugin *pluginWithConfig) {
	// Initialize the runner control members
	plugin.execRequestCh = make(chan func())
	plugin.execRequestChOpen = true
	plugin.execRequestChClosed = make(chan bool)
	plugin.runnerDoneCh = make(chan error)

	// Spin up a goroutine to execute callbacks for the plugin.
	go func() {
		fmt.Printf("%T plugin runner START\n", plugin.instance)
		// Signal completion before exiting.
		defer func() {
			close(plugin.runnerDoneCh)
			fmt.Printf("%T plugin runner STOP\n", plugin.instance)
		}()

		// Just keep executing until the channel closes.
		for f := range plugin.execRequestCh {
			f()
		}
	}()

}

func stopPluginRunner(plugin *pluginWithConfig) {
	if plugin.execRequestChOpen {
		// Mark that execRequestCh is no longer open, so we don't try to close it twice
		plugin.execRequestChOpen = false

		// The solution here is inspired by "multiple senders one receiver" at
		// https://go101.org/article/channel-closing.html

		// Next, signal that execRequestCh channel is about to close
		close(plugin.execRequestChClosed)

		// Wait for any pending send operations complete.  They'll either complete the send operations, or bail out on
		// the done signal.
		plugin.execRequestChSendOps.Wait()

		// Close the channel
		close(plugin.execRequestCh)

		// Finally, wait for the runner to indicate completion
		err := <-plugin.runnerDoneCh
		if err != nil {
			fmt.Printf("%T runner completed with error: %s\n", plugin.instance, err)
		}
	}
}

func (pmaas *PMAAS) configurePluginStaticContentDir(plugin *pluginWithConfig, serveMux *http.ServeMux) {
	pluginPath := "/" + pmaas.getPluginPath(plugin) + "/"
	pluginContentFS, staticContentDir := pmaas.getContentFS(plugin)

	if pluginContentFS == nil {
		fmt.Printf("Unable to serve static content for %s, plugin did not provide an fs.FS instance\n", pluginPath)
		return
	}

	pluginContentReadDirFs, ok := pluginContentFS.(fs.ReadDirFS)

	if !ok {
		fmt.Printf("Unable to serve static content for %s, fs.FS instance provided by plugin does not "+
			"implement fs.ReadDirFS\n", pluginPath)
		return
	}

	_, err := pluginContentReadDirFs.ReadDir(plugin.staticContentDir)

	if err != nil {
		fmt.Printf("Unable to serve %s from %s: %v\n", pluginPath, staticContentDir, err)
		return
	}

	pluginStaticContentFS, err := fs.Sub(pluginContentFS, plugin.staticContentDir)

	if err != nil {
		fmt.Printf("Unable to serve %s from %s: %v\n", pluginPath, staticContentDir, err)
		return
	}

	fmt.Printf("Serving static content for %s from %s\n",
		pluginPath, staticContentDir+"/"+plugin.staticContentDir)
	serveMux.Handle(pluginPath,
		http.StripPrefix(pluginPath, http.FileServer(dirWithLogger{delegate: http.FS(pluginStaticContentFS)})))
	serveMux.HandleFunc(pluginPath+"hello", hello)
}

func (pmaas *PMAAS) renderList(_ *pluginWithConfig, w http.ResponseWriter, r *http.Request,
	options spi.RenderListOptions, items []interface{}) {
	alt := r.URL.Query()["alt"]

	if len(alt) > 0 && alt[0] == "json" {
		pmaas.renderJsonList(w, r, items)
		return
	}

	var renderPlugin spi.IPMAASRenderPlugin = nil
	for _, plugin := range pmaas.plugins {
		candidate, ok := plugin.instance.(spi.IPMAASRenderPlugin)

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
	sourcePlugin *pluginWithConfig, templateInfo *spi.TemplateInfo) (compiledTemplate spi.CompiledTemplate, err error) {
	var templateEnginePlugin spi.IPMAASTemplateEnginePlugin = nil

	for _, plugin := range pmaas.plugins {
		candidate, ok := plugin.instance.(spi.IPMAASTemplateEnginePlugin)

		if ok {
			templateEnginePlugin = candidate
			break
		}
	}

	if templateEnginePlugin == nil {
		panic("No instance IPMAASTemplateEnginePlugin available")
	}

	contentFS, _ := pmaas.getContentFS(sourcePlugin)

	if contentFS == nil {
		panic(fmt.Sprintf("No fs.FS implementation available for plugin %s", pmaas.getPluginPath(sourcePlugin)))
	} else {
		//fmt.Printf("Loading template %s from %s\n", templateInfo.Name, contentFSDescription)
	}

	webPath := "/" + pmaas.getPluginPath(sourcePlugin)
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

func (pmaas *PMAAS) getPluginPath(plugin *pluginWithConfig) string {
	pluginType := plugin.pluginType
	return pluginType.PkgPath() + "/" + pluginType.Name()
}

func (pmaas *PMAAS) getContentFS(plugin *pluginWithConfig) (fs.FS, string) {

	if plugin.config.ContentPathOverride != "" {
		// The plugin config provided a path
		contentFs := os.DirFS(plugin.config.ContentPathOverride)
		return contentFs, fmt.Sprintf("os.DirFS(%s)", plugin.config.ContentPathOverride)
	}

	if pmaas.config.ContentPathRoot != "" {
		// The server has a configured content root.  Does it have content for this plugin?
		pluginPath := pmaas.getPluginPath(plugin)
		pluginContentDir := pmaas.config.ContentPathRoot + "/" + pluginPath
		fileInfo, err := os.Stat(pluginContentDir)

		if err == nil && fileInfo.IsDir() {
			// It does, so let's use it
			contentFs := os.DirFS(pluginContentDir)
			return contentFs, fmt.Sprintf("os.DirFS(%s)", pluginContentDir)
		}
	}

	return plugin.contentFS, fmt.Sprintf("%T(providedByPlugin)", plugin.contentFS)
}

func (pmaas *PMAAS) getEntityRenderer(_ *pluginWithConfig, entityType reflect.Type) (spi.EntityRenderer, error) {
	var rendererFactory spi.EntityRendererFactory

	for _, plugin := range pmaas.plugins {
		for _, entityRendererRegistration := range plugin.entityRenderers {
			if entityType.AssignableTo(entityRendererRegistration.entityType) {
				rendererFactory = entityRendererRegistration.rendererFactory
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
	sourcePlugin *pluginWithConfig,
	uniqueData string,
	entityType reflect.Type,
	name string) (string, error) {
	id := fmt.Sprintf("%s_%s_%s", sourcePlugin.pluginType.PkgPath(), sourcePlugin.pluginType.Name(), uniqueData)
	id = strings.ReplaceAll(id, " ", "_")
	err := pmaas.entityManager.AddEntity(id, entityType)

	if err != nil {
		return "", err
	}

	event := events.EntityRegisteredEvent{EntityEvent: events.EntityEvent{Id: id, EntityType: entityType, Name: name}}
	err = pmaas.eventManager.BroadcastEvent(pmaas.selfType, event)

	if err != nil {
		fmt.Printf("Unable to broadcast %s: %v", event, err)
	}

	return id, nil
}

func (pmaas *PMAAS) deregisterEntity(_ *pluginWithConfig, id string) error {
	return pmaas.entityManager.RemoveEntity(id)
}

func (pmaas *PMAAS) broadcastEvent(sourcePlugin *pluginWithConfig, event any) error {
	return pmaas.eventManager.BroadcastEvent(sourcePlugin.pluginType, event)
}

func (pmaas *PMAAS) registerEventReceiver(
	sourcePlugin *pluginWithConfig,
	predicate events.EventPredicate,
	receiver events.EventReceiver) (int, error) {
	return pmaas.eventManager.AddReceiver(sourcePlugin, predicate, receiver)
}

func (pmaas *PMAAS) deregisterEventReceiver(
	_ *pluginWithConfig, handle int) error {
	return pmaas.eventManager.RemoveReceiver(handle)
}

func genericEntityRenderer(entity any) (string, error) {
	return fmt.Sprintf("<div>%T</div>", entity), nil
}
