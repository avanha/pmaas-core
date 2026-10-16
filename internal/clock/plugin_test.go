package clock

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/avanha/pmaas-spi"
)

type fakeContainer struct {
	spi.IPMAASContainer

	mu         sync.Mutex
	broadcasts []any
	sources    []string
}

func (c *fakeContainer) BroadcastEvent(source string, event any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sources = append(c.sources, source)
	c.broadcasts = append(c.broadcasts, event)

	return nil
}

func (c *fakeContainer) ClosedCallbackChannel() chan func() {
	ch := make(chan func())
	close(ch)

	return ch
}

func (c *fakeContainer) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var kinds []string
	for _, event := range c.broadcasts {
		kinds = append(kinds, reflect.TypeOf(event).Name())
	}

	return kinds
}

// fixture is a plugin whose time moves only when the test says so.
type fixture struct {
	container *fakeContainer
	plugin    *Plugin
	ticks     chan time.Time
	waiting   chan struct{}

	// exited is whether the driver has finished. It takes a moment to once told to, so that what only returns
	// after it has finished can be told from what doesn't wait.
	exited atomic.Bool
}

func newFixture() *fixture {
	f := &fixture{
		container: &fakeContainer{},
		ticks:     make(chan time.Time),
		waiting:   make(chan struct{}, 16),
	}

	var mu sync.Mutex
	current := time.Date(2026, 3, 10, 10, 15, 20, 0, time.UTC)

	f.plugin = NewPlugin()
	f.plugin.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return current }
	f.plugin.wait = func(ctx context.Context, _ time.Duration) bool {
		f.exited.Store(false)
		f.waiting <- struct{}{}

		select {
		case <-ctx.Done():
			time.Sleep(100 * time.Millisecond)
			f.exited.Store(true)

			return false
		case next := <-f.ticks:
			mu.Lock()
			current = next
			mu.Unlock()

			return true
		}
	}

	f.plugin.Init(f.container)

	return f
}

// tick moves time on to the start of the given minute of the day, and waits for the driver to have taken it in.
func (f *fixture) tick(hour, minute int) {
	<-f.waiting
	f.ticks <- time.Date(2026, 3, 10, hour, minute, 0, 0, time.UTC)
}

func TestPlugin_BroadcastsAsSoonAsTheServerStartsIt(t *testing.T) {
	f := newFixture()
	f.plugin.Start()

	f.tick(10, 59)
	f.tick(11, 0)
	<-f.waiting // the driver has taken in the last tick when it's waiting for the next

	want := []string{"MinuteChangedEvent", "HourChangedEvent", "MinuteChangedEvent"}
	if got := f.container.kinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("broadcast %v, want %v", got, want)
	}

	for _, source := range f.container.sources {
		if source != eventSourceId {
			t.Errorf("an event was broadcast from %q", source)
		}
	}

	<-f.plugin.Stop()
}

func TestPlugin_StoppingItWaitsForTheDriverAndNothingIsBroadcastAfter(t *testing.T) {
	f := newFixture()
	f.plugin.Start()
	<-f.waiting

	done := f.plugin.Stop()

	// The channel closes once the driver has finished.
	if _, open := <-done; open {
		t.Fatal("received a callback, wanted the channel closed")
	}

	if !f.exited.Load() {
		t.Error("the channel was closed before the driver had finished")
	}

	select {
	case f.ticks <- time.Date(2026, 3, 10, 11, 1, 0, 0, time.UTC):
		t.Fatal("a stopped clock is still waiting for time to pass")
	case <-time.After(50 * time.Millisecond):
	}

	if len(f.container.kinds()) != 0 {
		t.Error("a stopped clock broadcast something")
	}
}

func TestPlugin_StoppingOneThatNeverStartedNeedsNothingWaitedFor(t *testing.T) {
	f := newFixture()

	if _, open := <-f.plugin.Stop(); open {
		t.Fatal("received a callback, wanted the channel closed")
	}
}

func TestPlugin_ShortName(t *testing.T) {
	if got := NewPlugin().ShortName(); got != "clock" {
		t.Errorf("got %q", got)
	}
}
