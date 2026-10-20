package config

type PluginConfig struct {
	ContentPathOverride string
	StaticContentDir    string `default:"static"`

	// MenuPriority replaces the priority the plugin gives its navigation menu entry (spi.HttpHandlerOptions.MenuPriority): the higher
	// the number, the earlier, and entries above 0 are set apart from the rest by a rule. Nil keeps the plugin's own. For the plugin
	// that provides the status page, it is the priority of the Status entry.
	MenuPriority *int
}
