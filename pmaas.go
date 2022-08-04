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

type PMAAS struct {
	config  *Config
	plugins []spi.IPMAASPlugin
}

func NewPMAAS(config *Config) *PMAAS {
	return &PMAAS{config: config}
}

func hello(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(w, "hello from pmaas\n")
}

func listPlugins(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(w, "plugins:\n")
}

func (pmaas *PMAAS) ListenAndServe() {
	http.HandleFunc("/", hello)
	http.HandleFunc("/plugin", listPlugins)
	http.ListenAndServe(fmt.Sprintf(":%d", pmaas.config.HttpPort), nil)
}
