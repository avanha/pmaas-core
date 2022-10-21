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
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"pmaas.io/spi"
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
	entityType               reflect.Type
	rendererFactory          spi.EntityRendererFactory
	streamingRendererFactory spi.StreamingEntityRendererFactory
}

type pluginWithConfig struct {
	config          *PluginConfig
	instance        spi.IPMAASPlugin
	httpHandlers    []*httpHandlerRegistration
	entityRenderers []entityRendererRegistration
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
	}

	if c.plugins == nil {
		c.plugins = []*pluginWithConfig{wrapper}
	} else {
		c.plugins = append(c.plugins, wrapper)
	}
}

type containerAdapter struct {
	pmaas  *PMAAS
	target *pluginWithConfig
}

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

func (ca *containerAdapter) RegisterStreamingEntityRenderer(entityType reflect.Type, streamingRendererFactory spi.StreamingEntityRendererFactory) {
	registration := entityRendererRegistration{
		entityType:               entityType,
		streamingRendererFactory: streamingRendererFactory,
	}
	ca.target.entityRenderers = append(ca.target.entityRenderers, registration)
}

func (ca *containerAdapter) RenderList(w http.ResponseWriter, r *http.Request, options spi.RenderListOptions, items []interface{}) {
	ca.pmaas.renderList(ca.target, w, r, options, items)
}

func (ca *containerAdapter) GetTemplate(templateInfo *spi.TemplateInfo) (spi.ITemplate, error) {
	return ca.pmaas.getTemplate(ca.target, templateInfo)
}

func (ca *containerAdapter) GetEntityRenderer(entityType reflect.Type) (spi.EntityRenderFunc, error) {
	return ca.pmaas.getEntityRenderer(ca.target, entityType)
}

type PMAAS struct {
	config  *Config
	plugins []*pluginWithConfig
}

func NewPMAAS(config *Config) *PMAAS {
	return &PMAAS{
		config:  config,
		plugins: config.plugins,
	}
}

func hello(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "Hello!\n")
}

func (pmaas *PMAAS) Run() {
	mainCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer func() {
		fmt.Printf("Executing deferred stop()\n")
		stop()
	}()

	fmt.Printf("Initializing...\n")
	// Init plugins
	for _, plugin := range pmaas.plugins {
		plugin.instance.Init(&containerAdapter{
			pmaas:  pmaas,
			target: plugin})
	}

	fmt.Printf("Starting...\n")

	// Start plugins
	for _, plugin := range pmaas.plugins {
		plugin.instance.Start()
	}

	fmt.Printf("Running...\n")

	g, gCtx := errgroup.WithContext(mainCtx)

	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/hello", hello)
	//serveMux.HandleFunc("/plugin", listPlugins)

	for _, plugin := range pmaas.plugins {
		fmt.Printf("Plugin %T config: %v\n", plugin.instance, plugin.config)
		pluginPath := "/" + pmaas.getPluginPath(plugin) + "/"
		staticContentDir := pmaas.getContentRoot(plugin) + "/" + plugin.config.StaticContentDir
		_, err := os.Stat(staticContentDir)

		if err == nil {
			fmt.Printf("Serving %s from %s\n", pluginPath, staticContentDir)
			serveMux.Handle(pluginPath,
				http.StripPrefix(pluginPath, http.FileServer(dirWithLogger{delegate: http.Dir(staticContentDir)})))
			serveMux.HandleFunc(pluginPath+"hello", hello)
		} else {
			fmt.Printf("Unable to serve %s from %s: %v\n", pluginPath, staticContentDir, err)
		}

		for _, httpRegistration := range plugin.httpHandlers {
			serveMux.HandleFunc(httpRegistration.pattern, httpRegistration.handlerFunc)
		}
	}

	fmt.Printf("serverMux: %v", serveMux)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", pmaas.config.HttpPort),
		Handler: serveMux,
	}

	g.Go(func() error {
		fmt.Printf("HTTP Server starting...\n")
		var err = httpServer.ListenAndServe()

		if err == nil || err == http.ErrServerClosed {
			fmt.Printf("HTTP Server stopped...\n")
			err = nil
		} else {
			fmt.Printf("HTTP Server stopped with error: %s\n", err)
		}
		return err
	})

	time.Sleep(100 * time.Millisecond)

	g.Go(func() error {
		fmt.Printf("Shutdown task waiting for signal...\n")
		<-gCtx.Done()
		shutdownHttp(httpServer)
		stopPlugins(pmaas.plugins)
		return nil
	})

	if err := g.Wait(); err != nil {
		fmt.Printf("Stopped with error: %s \n", err)

	}

	go func() {
		<-mainCtx.Done()
		fmt.Printf("mainCtx is done\n")
	}()

	fmt.Printf("Done\n")
}

func shutdownHttp(httpServer *http.Server) {
	fmt.Printf("HTTP Server shutdown started...\n")
	var err = httpServer.Shutdown(context.Background())

	if err == nil {
		fmt.Printf("HTTP Server shutdown complete...\n")
	} else {
		fmt.Printf("HTTP Server shutdown completd with error: %s\n", err)
	}
}

func stopPlugins(plugins []*pluginWithConfig) {
	fmt.Printf("Plugin shutdown started...\n")
	// Stop plugins
	for _, plugin := range plugins {
		plugin.instance.Stop()
	}
	fmt.Printf("Plugin shutdown complete...\n")
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

func (pmaas *PMAAS) getTemplate(sourcePlugin *pluginWithConfig, templateInfo *spi.TemplateInfo) (spi.ITemplate, error) {
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
	updatedPaths := make([]string, len(templateInfo.Paths))

	for i, path := range templateInfo.Paths {
		updatedPaths[i] = root + "/" + path
	}

	updatedTemplateInfo := &spi.TemplateInfo{
		Name:    templateInfo.Name,
		FuncMap: templateInfo.FuncMap,
		Paths:   updatedPaths,
	}

	return templateEnginePlugin.GetTemplate(updatedTemplateInfo)
}

func (pmaas *PMAAS) getPluginPath(plugin *pluginWithConfig) string {
	pluginType := reflect.TypeOf(plugin.instance)

	if pluginType.Kind() == reflect.Ptr {
		pluginType = reflect.ValueOf(plugin.instance).Elem().Type()
	}

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

func (pmaas *PMAAS) getEntityRenderer(sourcePlugin *pluginWithConfig, entityType reflect.Type) (spi.EntityRenderFunc, error) {
	var rendererFactory spi.EntityRendererFactory
	var streamingRendererFactory spi.StreamingEntityRendererFactory

	for _, plugin := range pmaas.plugins {
		for _, entityRendererRegistration := range plugin.entityRenderers {
			if entityType.AssignableTo(entityRendererRegistration.entityType) {
				rendererFactory = entityRendererRegistration.rendererFactory
				streamingRendererFactory = entityRendererRegistration.streamingRendererFactory
			}
		}
	}

	if rendererFactory != nil {
		return rendererFactory()
	}

	if streamingRendererFactory != nil {
		streamingRender, err := streamingRendererFactory()

		if err != nil {
			return nil, fmt.Errorf("streamingRendererFactory failed: %w", err)
		}

		wrapperFunc := func(entity any) (string, error) {
			var buffer bytes.Buffer
			err := streamingRender(&buffer, entity)

			if err != nil {
				return "", fmt.Errorf("error executing StreamingEntityRenderFunc: %v", err)
			}

			return buffer.String(), nil
		}

		return wrapperFunc, nil
	}

	return genericEntityRenderer, nil
}

func genericEntityRenderer(entity any) (string, error) {
	return fmt.Sprintf("<div>%T</div>", entity), nil
}
