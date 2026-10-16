package core

import (
	"testing"

	"github.com/avanha/pmaas-core/config"
	"github.com/avanha/pmaas-spi"
)

type namedPlugin struct{ spi.IPMAASPlugin }

func (namedPlugin) ShortName() string { return "other" }

func TestWithClock_PutsTheServersClockFirst(t *testing.T) {
	conf := config.NewConfig()
	conf.AddPlugin(namedPlugin{}, config.PluginConfig{})

	plugins := withClock(conf)

	if len(plugins) != 2 || plugins[0].Instance.ShortName() != "clock" || plugins[1].Instance.ShortName() != "other" {
		t.Fatalf("plugins: %v", plugins)
	}

	if len(conf.Plugins()) != 1 {
		t.Errorf("the configuration's own plugins were changed: %d", len(conf.Plugins()))
	}
}
