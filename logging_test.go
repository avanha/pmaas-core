package core

import (
	"context"
	"log/slog"
	"testing"

	"github.com/avanha/pmaas-core/config"
	"github.com/avanha/pmaas-spi/logging"
)

func levelOf(c logging.Controller, name string) (slog.Level, bool) {
	for _, component := range c.Components() {
		if component.Name == name {
			return component.Level, true
		}
	}

	return 0, false
}

// A level set while the server runs is the level after it is started again, and a first start is quiet.
func TestLogLevels_AreKeptAcrossRestarts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	first := NewPMAAS(config.NewConfig())

	if level, _ := levelOf(first.logRegistry, ""); level != slog.LevelWarn {
		t.Fatalf("the default is %v", level)
	}

	if err := first.logRegistry.SetLevel("core.eventmanager", slog.LevelDebug); err != nil {
		t.Fatal(err)
	}

	if err := first.logRegistry.SetLevel("", slog.LevelError); err != nil {
		t.Fatal(err)
	}

	second := NewPMAAS(config.NewConfig())

	if level, ok := levelOf(second.logRegistry, "core.eventmanager"); !ok || level != slog.LevelDebug {
		t.Errorf("event manager: %v %v", level, ok)
	}

	if level, _ := levelOf(second.logRegistry, ""); level != slog.LevelError {
		t.Errorf("default: %v", level)
	}

	// The loggers the server made for itself already have it.
	if !second.logRegistry.Logger("core.eventmanager").Enabled(context.Background(), slog.LevelDebug) {
		t.Error("the event manager's logger didn't take the kept level")
	}
}

// The container is what a plugin asks for its logger and the controller.
func TestContainerAdapter_IsALoggingProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	server := NewPMAAS(config.NewConfig())
	ca := &containerAdapter{pmaas: server, target: server.plugins[0]}

	if logging.ControllerOf(ca) == nil {
		t.Fatal("no controller")
	}

	log := ca.Logger("part")

	if !log.Enabled(context.Background(), slog.LevelWarn) || log.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("a plugin's logger isn't at warn")
	}

	if _, ok := levelOf(ca.LogController(), "plugin.clock.part"); !ok {
		t.Errorf("the component isn't listed: %+v", ca.LogController().Components())
	}
}
