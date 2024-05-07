package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strings"
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
	pluginType       reflect.Type
}

type Config struct {
	ContentPathRoot string
	HttpPort        int
	plugins         []*pluginWithConfig
}

type dirWithLogger struct {
	delegate http.Dir
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
		config:          &config,
		instance:        plugin,
		httpHandlers:    make([]*httpHandlerRegistration, 0),
		entityRenderers: make([]entityRendererRegistration, 0),
		pluginType:      getPluginType(plugin),
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

// A containerAdapter is an implementation of spi.IPMAASContainer.  It wraps the reference to the PMAAS server along
// with the plugin instance and its config.  This allows us to track the plugin calling into the server.
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

func (ca *containerAdapter) RegisterEntity(uniqueData string, entityType reflect.Type) (string, error) {
	return ca.pmaas.registerEntity(ca.target, uniqueData, entityType)
}

func (ca *containerAdapter) DeregisterEntity(id string) error {
	return ca.pmaas.deregisterEntity(ca.target, id)
}

func (ca *containerAdapter) RegisterEventReceiver(
	predicate events.EventPredicate,
	receiver events.EventReceiver) (int, error) {
	return ca.pmaas.registerEventReceiver(ca.target, predicate, receiver)
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

func hello(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "Hello!\n")
}

func (pmaas *PMAAS) Run() error {
	fmt.Printf("pmaas.Run: Start\n")

	mainCtx, cancelFn := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelFn()

	fmt.Printf("Initializing...\n")
	// Init plugins
	for _, plugin := range pmaas.plugins {
		plugin.instance.Init(&containerAdapter{
			pmaas:  pmaas,
			target: plugin})
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

	var httpServer *HttpServer
	httpServer, err = pmaas.startHttpServer()
	if err != nil {
		return err
	}

	fmt.Printf("pmaas.Run: Starting plugins...\n")

	// Start plugins
	for _, plugin := range pmaas.plugins {
		plugin.instance.Start()
	}

	// Wait for the done signal
	fmt.Printf("pmaas.Run: Running, waiting for done sginal...\n")
	<-mainCtx.Done()

	fmt.Printf("pmaas.Run: Done sginal recevied, stopping...\n")
	fmt.Printf("pmaas.Run: Stopping plugins...\n")
	stopPlugins(pmaas.plugins)

	fmt.Printf("pmaas.Run: Stopping core services...\n")

	stopHttpServer(httpServer)
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
	for _, plugin := range plugins {
		plugin.instance.Stop()
	}
}

func (pmaas *PMAAS) configurePluginStaticContentDir(plugin *pluginWithConfig, serveMux *http.ServeMux) {
	pluginPath := "/" + pmaas.getPluginPath(plugin) + "/"
	staticContentDir := pmaas.getContentRoot(plugin) + "/" + plugin.staticContentDir
	_, err := os.Stat(staticContentDir)

	if err == nil {
		fmt.Printf("Serving %s from %s\n", pluginPath, staticContentDir)
		serveMux.Handle(pluginPath,
			http.StripPrefix(pluginPath, http.FileServer(dirWithLogger{delegate: http.Dir(staticContentDir)})))
		serveMux.HandleFunc(pluginPath+"hello", hello)
	} else {
		fmt.Printf("Unable to serve %s from %s: %v\n", pluginPath, staticContentDir, err)
	}
}

func (pmaas *PMAAS) renderList(sourcePlugin *pluginWithConfig, w http.ResponseWriter, r *http.Request,
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

func (pmaas *PMAAS) renderJsonList(w http.ResponseWriter, r *http.Request, items []interface{}) {
	bytes, err := json.MarshalIndent(items, "", "  ")

	if err == nil {
		w.Write(bytes)
	}
}

func (pmaas *PMAAS) getTemplate(
	sourcePlugin *pluginWithConfig, templateInfo *spi.TemplateInfo) (spi.CompiledTemplate, error) {
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

	root := pmaas.getContentRoot(sourcePlugin)
	webPath := "/" + pmaas.getPluginPath(sourcePlugin)
	updatedPaths := make([]string, len(templateInfo.Paths))
	updatedScripts := make([]string, len(templateInfo.Scripts))
	updatedStyles := make([]string, len(templateInfo.Styles))

	for i, path := range templateInfo.Paths {
		updatedPaths[i] = root + "/" + path
	}

	for i, script := range templateInfo.Scripts {
		updatedScripts[i] = webPath + "/" + script
	}

	for i, style := range templateInfo.Styles {
		updatedStyles[i] = webPath + "/" + style
	}

	updatedTemplateInfo := spi.TemplateInfo{
		Name:    templateInfo.Name,
		FuncMap: templateInfo.FuncMap,
		Paths:   updatedPaths,
		Scripts: updatedScripts,
		Styles:  updatedStyles,
	}

	return templateEnginePlugin.GetTemplate(&updatedTemplateInfo)
}

func (pmaas *PMAAS) getPluginPath(plugin *pluginWithConfig) string {
	pluginType := plugin.pluginType
	return pluginType.PkgPath() + "/" + pluginType.Name()
}

func (pmaas *PMAAS) getContentRoot(plugin *pluginWithConfig) string {
	var root string

	if plugin.config.ContentPathOverride == "" {
		root = pmaas.config.ContentPathRoot
		pluginPath := pmaas.getPluginPath(plugin)
		root = root + "/" + pluginPath
	} else {
		root = plugin.config.ContentPathOverride
	}

	return root
}

func (pmaas *PMAAS) getEntityRenderer(sourcePlugin *pluginWithConfig, entityType reflect.Type) (spi.EntityRenderer, error) {
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

func (pmaas *PMAAS) registerEntity(sourcePlugin *pluginWithConfig, uniqueData string, entityType reflect.Type) (string, error) {
	id := fmt.Sprintf("%s_%s_%s", sourcePlugin.pluginType.PkgPath(), sourcePlugin.pluginType.Name(), uniqueData)
	id = strings.ReplaceAll(id, " ", "_")
	err := pmaas.entityManager.AddEntity(id, entityType)

	if err != nil {
		return "", err
	}

	pmaas.eventManager.BroadcastEvent(pmaas.selfType, events.EntityRegisteredEvent{Id: id, EntityType: entityType})

	return id, nil
}

func (pmaas *PMAAS) deregisterEntity(sourcePlugin *pluginWithConfig, id string) error {
	return pmaas.entityManager.RemoveEntity(id)
}

func (pmaas *PMAAS) registerEventReceiver(
	sourcePlugin *pluginWithConfig,
	predicate events.EventPredicate,
	receiver events.EventReceiver) (int, error) {
	return pmaas.eventManager.AddReceiver(predicate, receiver)
}

func genericEntityRenderer(entity any) (string, error) {
	return fmt.Sprintf("<div>%T</div>", entity), nil
}
