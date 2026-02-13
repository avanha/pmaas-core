package config

import (
	"github.com/avanha/pmaas-spi"
)

type Config struct {
	ContentPathRoot string
	HttpPort        int
	plugins         []PluginWithConfig
}

func NewConfig() *Config {
	return &Config{
		ContentPathRoot: "/var/pmaas/content",
		HttpPort:        8090,
		plugins:         make([]PluginWithConfig, 0),
	}
}

func (c *Config) AddPlugin(plugin spi.IPMAASPlugin, config PluginConfig) {
	c.plugins = append(c.plugins, NewPluginWithConfig(plugin, config))
}

func (c *Config) Plugins() []PluginWithConfig {
	return c.plugins
}
