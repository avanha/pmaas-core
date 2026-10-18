// Package logging is the server's loggers: one per component, each with a level that can be changed while the server runs,
// and that is kept (see pmaas-spi/logging for how components and levels work).
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	spilogging "github.com/avanha/pmaas-spi/logging"
)

// DefaultLevel is the level of everything until it is told otherwise.
const DefaultLevel = slog.LevelWarn

// Settings are the levels as they are kept.
type Settings struct {
	Default slog.Level
	Levels  map[string]slog.Level
}

// Registry makes the loggers and holds their levels. It is safe to use from any goroutine.
type Registry struct {
	out  slog.Handler
	save func(Settings) error

	mu       sync.Mutex
	def      slog.Level
	explicit map[string]slog.Level
	comps    map[string]*component
}

var _ spilogging.Controller = (*Registry)(nil)

// component is what its loggers consult, on every message, so that a change of level takes effect at once.
type component struct {
	level atomic.Int64
}

// NewRegistry makes a registry whose loggers write to w. save, if not nil, is called with the settings whenever they change.
func NewRegistry(w io.Writer, save func(Settings) error) *Registry {
	return &Registry{
		// The handlers do the filtering, so the level of this one is everything.
		out:      slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.Level(-1 << 20)}),
		save:     save,
		def:      DefaultLevel,
		explicit: make(map[string]slog.Level),
		comps:    make(map[string]*component),
	}
}

// Restore takes the settings that were kept. Levels that are not ones that can be set, and names that are not names, are
// dropped, so that a file that was edited by hand can't break anything.
func (r *Registry) Restore(s Settings) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if spilogging.ValidLevel(s.Default) {
		r.def = s.Default
	}

	for name, level := range s.Levels {
		if spilogging.Valid(name) && spilogging.ValidLevel(level) {
			r.explicit[name] = level
		}
	}

	r.recompute()
}

// Logger returns the logger for the component. Asking again gets another logger with the same level.
func (r *Registry) Logger(name string) *slog.Logger {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.comps[name]
	if !ok {
		c = &component{}
		c.level.Store(int64(r.effective(name)))
		r.comps[name] = c
	}

	return slog.New(&handler{comp: c, out: r.out.WithAttrs([]slog.Attr{slog.String("component", name)})})
}

// PluginLogger is Logger for a plugin's component, "plugin.<shortName>", or of a part of it, "plugin.<shortName>.<part>".
func (r *Registry) PluginLogger(shortName string, names ...string) *slog.Logger {
	return r.Logger(strings.Join(append([]string{"plugin", shortName}, names...), "."))
}

// effective is the level of a component: the one set for it or for the nearest above it, or else the default. The caller
// holds the lock.
func (r *Registry) effective(name string) slog.Level {
	for {
		if level, ok := r.explicit[name]; ok {
			return level
		}

		i := strings.LastIndexByte(name, '.')
		if i < 0 {
			return r.def
		}

		name = name[:i]
	}
}

// recompute brings every component up to date with the settings. The caller holds the lock.
func (r *Registry) recompute() {
	for name, c := range r.comps {
		c.level.Store(int64(r.effective(name)))
	}
}

func (r *Registry) Components() []spilogging.Component {
	r.mu.Lock()
	defer r.mu.Unlock()

	names := map[string]bool{}
	for name := range r.comps {
		names[name] = true
	}

	for name := range r.explicit {
		names[name] = true
	}

	result := []spilogging.Component{{Name: "", Level: r.def, Set: true}}

	for name := range names {
		// A level set for a component above one that has not asked for a logger is shown too: it is what is
		// inherited from, and what the person who set it is looking for.
		_, set := r.explicit[name]
		result = append(result, spilogging.Component{Name: name, Level: r.effective(name), Set: set})
	}

	spilogging.SortByName(result)

	return result
}

func (r *Registry) SetLevel(name string, level slog.Level) error {
	if name != "" && !spilogging.Valid(name) {
		return fmt.Errorf("%q is not a component name", name)
	}

	if !spilogging.ValidLevel(level) {
		return fmt.Errorf("%v is not a level that can be set", level)
	}

	return r.change(func() {
		if name == "" {
			r.def = level
		} else {
			r.explicit[name] = level
		}
	})
}

func (r *Registry) ClearLevel(name string) error {
	if name == "" {
		return nil
	}

	if !spilogging.Valid(name) {
		return fmt.Errorf("%q is not a component name", name)
	}

	return r.change(func() { delete(r.explicit, name) })
}

// change makes a change to the settings, applies it and keeps it. The levels are applied even if keeping them fails, which
// the error says.
func (r *Registry) change(apply func()) error {
	r.mu.Lock()
	apply()
	r.recompute()
	settings := r.settings()
	r.mu.Unlock()

	if r.save == nil {
		return nil
	}

	if err := r.save(settings); err != nil {
		return fmt.Errorf("the level was changed but could not be kept: %w", err)
	}

	return nil
}

// settings is a copy of the settings. The caller holds the lock.
func (r *Registry) settings() Settings {
	levels := make(map[string]slog.Level, len(r.explicit))
	for name, level := range r.explicit {
		levels[name] = level
	}

	return Settings{Default: r.def, Levels: levels}
}

// handler is a slog.Handler that passes to the writer what its component's level lets through.
type handler struct {
	comp *component
	out  slog.Handler
}

func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return int64(level) >= h.comp.level.Load()
}

func (h *handler) Handle(ctx context.Context, record slog.Record) error {
	return h.out.Handle(ctx, record)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{comp: h.comp, out: h.out.WithAttrs(attrs)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{comp: h.comp, out: h.out.WithGroup(name)}
}
