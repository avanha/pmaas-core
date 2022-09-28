package core

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"pmaas.io/spi"
)

type PluginConfig struct {
}

type httpHandlerRegistration struct {
	pattern     string
	handlerFunc http.HandlerFunc
}

type pluginWithConfig struct {
	config       *PluginConfig
	instance     spi.IPMAASPlugin
	httpHandlers []*httpHandlerRegistration
}

type Config struct {
	HttpPort int
	plugins  []*pluginWithConfig
}

func NewConfig() *Config {
	return &Config{
		HttpPort: 8090,
		plugins:  nil,
	}
}

func (c *Config) AddPlugin(plugin spi.IPMAASPlugin, config PluginConfig) {
	var wrapper = &pluginWithConfig{
		config:       &config,
		instance:     plugin,
		httpHandlers: make([]*httpHandlerRegistration, 0),
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

func (ca *containerAdapter) RenderList(w http.ResponseWriter, r *http.Request, items []interface{}) {
	ca.pmaas.renderList(ca.target, w, r, items)
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

func hello(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(w, "hello from pmaas\n")
}

func listPlugins(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(w, "plugins:\n")
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
	//http.HandleFunc("/", hello)
	//http.HandleFunc("/plugin", listPlugins)

	g, gCtx := errgroup.WithContext(mainCtx)

	serveMux := http.NewServeMux()

	for _, plugin := range pmaas.plugins {
		for _, httpRegistration := range plugin.httpHandlers {
			serveMux.HandleFunc(httpRegistration.pattern, httpRegistration.handlerFunc)
		}
	}

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

func (pmaas *PMAAS) renderList(sourcePlugin *pluginWithConfig, w http.ResponseWriter, r *http.Request, items []interface{}) {
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

	renderPlugin.RenderList(w, r, items)
}
