package config

import (
	"reflect"

	"pmaas.io/spi"
)

type PluginWithConfig struct {
	Config     PluginConfig
	Instance   spi.IPMAASPlugin
	PluginType reflect.Type
}

func NewPluginWithConfig(plugin spi.IPMAASPlugin, config PluginConfig) PluginWithConfig {
	return PluginWithConfig{
		Config:     config,
		Instance:   plugin,
		PluginType: getPluginType(plugin),
	}
}

func getPluginType(plugin spi.IPMAASPlugin) reflect.Type {
	pluginType := reflect.TypeOf(plugin)

	if pluginType.Kind() == reflect.Ptr {
		pluginType = reflect.ValueOf(plugin).Elem().Type()
	}

	return pluginType
}
