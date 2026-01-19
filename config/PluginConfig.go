package config

type PluginConfig struct {
	ContentPathOverride string
	StaticContentDir    string `default:"static"`
}
