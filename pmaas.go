package core

import (
	"fmt"
	"net/http"

	"pmaas.io/spi"
)

type PluginConfig struct {
}

type pluginWithConfig struct {
	config   *PluginConfig
	instance spi.IPMAASPlugin
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
		config:   &config,
		instance: plugin,
	}

	if c.plugins == nil {
		c.plugins = []*pluginWithConfig{wrapper}
	} else {
		c.plugins = append(c.plugins, wrapper)
	}
}

type containerAdapter struct {
	target *pluginWithConfig
}

type PMAAS struct {
	config  *Config
	plugins []*pluginWithConfig
}

func NewPMAAS(config *Config) *PMAAS {
	return &PMAAS{
		config: config,
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
	fmt.Printf("Initializing...\n")
	// Init plugins
	for _, plugin := range pmaas.plugins {
		plugin.instance.Init(&containerAdapter{plugin})
	}

	fmt.Printf("Starting...\n")

	// Start plugins
	for _, plugin := range pmaas.plugins {
		plugin.instance.Start()
	}

	fmt.Printf("Running...\n")
	http.HandleFunc("/", hello)
	http.HandleFunc("/plugin", listPlugins)
	http.ListenAndServe(fmt.Sprintf(":%d", pmaas.config.HttpPort), nil)
}
