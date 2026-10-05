package config

import (
	"github.com/avanha/pmaas-spi"
)

type Config struct {
	ContentPathRoot string
	HttpPort        int

	// BaseURLs lists every externally-reachable base URL (scheme://host[:port]) this server may be
	// addressed as, e.g. "http://localhost:8090" for local development and "https://myhome.example.com"
	// for a real deployment. Each entry must exactly match a redirect URI registered with any OAuth
	// provider plugins use, since IPMAASContainer.GetBaseUrl picks the entry matching an incoming
	// request's Host header rather than deriving scheme/host from the request itself.
	//
	// Leave it empty (the default) to get the one address that's always right for development: the
	// localhost address the server actually listens on, so it follows HttpPort, and https if a plugin
	// has provided a TLS certificate (see IPMAASContainer.ProvideTLSCertificate) and http if not. Set it
	// explicitly for any other hostname the server is reached by.
	BaseURLs []string

	plugins []PluginWithConfig
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
