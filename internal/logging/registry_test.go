package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	spilogging "github.com/avanha/pmaas-spi/logging"
)

func TestLogger_IsWarnUntilTold(t *testing.T) {
	var out bytes.Buffer
	log := NewRegistry(&out, nil).Logger("core.x")

	log.Debug("d")
	log.Info("i")
	log.Warn("w")
	log.Error("e")

	got := out.String()
	if strings.Contains(got, "msg=d") || strings.Contains(got, "msg=i") || !strings.Contains(got, "msg=w") || !strings.Contains(got, "msg=e") {
		t.Errorf("logged:\n%s", got)
	}

	if !strings.Contains(got, "component=core.x") {
		t.Errorf("the component isn't in the line:\n%s", got)
	}
}

func TestSetLevel_TakesEffectOnLoggersAlreadyMade(t *testing.T) {
	var out bytes.Buffer
	r := NewRegistry(&out, nil)
	log := r.Logger("plugin.esphome.broker")

	for _, tc := range []struct {
		name  string
		level slog.Level
		want  bool
	}{
		{"plugin.esphome.broker", slog.LevelDebug, true}, // its own
		{"plugin.esphome", slog.LevelDebug, true},        // one above it, once its own is cleared
	} {
		out.Reset()

		if tc.name != "plugin.esphome.broker" {
			_ = r.ClearLevel("plugin.esphome.broker")
		}

		if err := r.SetLevel(tc.name, tc.level); err != nil {
			t.Fatal(err)
		}

		log.Debug("hello")

		if got := strings.Contains(out.String(), "msg=hello"); got != tc.want {
			t.Errorf("%s: logged=%v", tc.name, got)
		}
	}
}

func TestLevels_OwnBeatsAncestorBeatsDefault(t *testing.T) {
	r := NewRegistry(&bytes.Buffer{}, nil)
	r.Logger("a.b.c")
	r.Logger("a.x")
	r.Logger("z")

	_ = r.SetLevel("a", slog.LevelInfo)
	_ = r.SetLevel("a.b.c", slog.LevelError)
	_ = r.SetLevel("", slog.LevelDebug)

	want := map[string]struct {
		level slog.Level
		set   bool
	}{
		"":      {slog.LevelDebug, true},
		"a":     {slog.LevelInfo, true},
		"a.b.c": {slog.LevelError, true},
		"a.x":   {slog.LevelInfo, false},
		"z":     {slog.LevelDebug, false},
	}

	got := r.Components()
	if len(got) != len(want) {
		t.Fatalf("components %+v", got)
	}

	for _, c := range got {
		if w := want[c.Name]; c.Level != w.level || c.Set != w.set {
			t.Errorf("%q: %+v, want %+v", c.Name, c, w)
		}
	}

	if got[0].Name != "" || got[1].Name != "a" {
		t.Errorf("not in order: %+v", got)
	}
}

func TestClearLevel_ReturnsToInheriting(t *testing.T) {
	r := NewRegistry(&bytes.Buffer{}, nil)
	r.Logger("a.b")
	_ = r.SetLevel("a.b", slog.LevelDebug)
	_ = r.ClearLevel("a.b")

	for _, c := range r.Components() {
		if c.Name == "a.b" && (c.Level != DefaultLevel || c.Set) {
			t.Errorf("%+v", c)
		}
	}

	if err := r.ClearLevel(""); err != nil {
		t.Errorf("clearing the default: %v", err)
	}
}

func TestSetLevel_RefusesWhatIsNotAThing(t *testing.T) {
	r := NewRegistry(&bytes.Buffer{}, nil)

	if r.SetLevel("a b", slog.LevelDebug) == nil || r.SetLevel("a", slog.Level(3)) == nil || r.ClearLevel("a..b") == nil {
		t.Error("accepted")
	}
}

func TestChange_IsKeptAndAFailureToKeepIsSaidButStillApplies(t *testing.T) {
	var saved []Settings

	var failWith error

	r := NewRegistry(&bytes.Buffer{}, func(s Settings) error {
		saved = append(saved, s)
		return failWith
	})

	_ = r.SetLevel("a", slog.LevelDebug)
	_ = r.SetLevel("", slog.LevelError)
	_ = r.ClearLevel("a")

	if len(saved) != 3 || saved[0].Levels["a"] != slog.LevelDebug || saved[1].Default != slog.LevelError ||
		len(saved[2].Levels) != 0 || saved[2].Default != slog.LevelError {
		t.Fatalf("saved %+v", saved)
	}

	// What was handed over is a copy, not the registry's own map.
	saved[1].Levels["b"] = slog.LevelInfo

	for _, c := range r.Components() {
		if c.Name == "b" {
			t.Error("the saved settings share the registry's map")
		}
	}

	failWith = errors.New("disk full")

	if err := r.SetLevel("a", slog.LevelInfo); err == nil || !errors.Is(err, failWith) {
		t.Errorf("err %v", err)
	}

	for _, c := range r.Components() {
		if c.Name == "a" && c.Level != slog.LevelInfo {
			t.Errorf("not applied: %+v", c)
		}
	}
}

func TestRestore_AppliesWhatWasKeptAndIgnoresWhatIsBad(t *testing.T) {
	var out bytes.Buffer
	r := NewRegistry(&out, nil)
	log := r.Logger("plugin.p")

	r.Restore(Settings{Default: slog.LevelInfo, Levels: map[string]slog.Level{
		"plugin.p": slog.LevelDebug, "bad name": slog.LevelDebug, "plugin.q": slog.Level(3),
	}})

	log.Debug("hello")

	if !strings.Contains(out.String(), "msg=hello") {
		t.Error("the restored level isn't in force on a logger made before")
	}

	var names []string
	for _, c := range r.Components() {
		names = append(names, c.Name)
	}

	if strings.Join(names, ",") != ",plugin.p" {
		t.Errorf("components %v", names)
	}

	// An unusable default is left as it was.
	r2 := NewRegistry(&out, nil)
	r2.Restore(Settings{Default: slog.Level(7)})

	if r2.Components()[0].Level != DefaultLevel {
		t.Error("bad default taken")
	}
}

func TestPluginLogger_NamesTheComponent(t *testing.T) {
	var out bytes.Buffer
	r := NewRegistry(&out, nil)

	r.PluginLogger("esphome", "broker").Warn("x")
	r.PluginLogger("netmon").Warn("y")

	if !strings.Contains(out.String(), "component=plugin.esphome.broker") || !strings.Contains(out.String(), "component=plugin.netmon\n") {
		t.Errorf("%s", out.String())
	}

	var _ spilogging.Controller = r
}

func TestWithAttrsKeepsTheLevel(t *testing.T) {
	var out bytes.Buffer
	r := NewRegistry(&out, nil)
	log := r.Logger("a").With("device", "d1")

	_ = r.SetLevel("a", slog.LevelInfo)

	log.Info("hi")

	if !strings.Contains(out.String(), "device=d1") || !strings.Contains(out.String(), "msg=hi") {
		t.Errorf("%s", out.String())
	}
}
