package clock

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/avanha/pmaas-core/config"
	"github.com/avanha/pmaas-spi"
	spiclock "github.com/avanha/pmaas-spi/clock"
)

// fakeContainer runs "the plugin goroutine" as a goroutine of its own, so that what's asked of the plugin from
// elsewhere is done where the real thing would do it.
type fakeContainer struct {
	spi.IPMAASContainer

	work chan func()

	mu          sync.Mutex
	registered  []reflect.Type
	deregisters []string
	broadcasts  []any
	broadcastId []string
	server      [][]func()
}

func newFakeContainer() *fakeContainer {
	c := &fakeContainer{work: make(chan func(), 16)}

	go func() {
		for f := range c.work {
			f()
		}
	}()

	return c
}

func (c *fakeContainer) close() { close(c.work) }

func (c *fakeContainer) EnqueueOnPluginGoRoutine(f func()) error {
	c.work <- f

	return nil
}

func (c *fakeContainer) EnqueueOnServerGoRoutine(invocations []func()) error {
	c.mu.Lock()
	c.server = append(c.server, invocations)
	c.mu.Unlock()

	return nil
}

func (c *fakeContainer) RegisterEntity(_ string, entityType reflect.Type, _ string, _ spi.EntityStubFactoryFunc) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.registered = append(c.registered, entityType)

	return "clock-entity-id", nil
}

func (c *fakeContainer) DeregisterEntity(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.deregisters = append(c.deregisters, id)

	return nil
}

func (c *fakeContainer) BroadcastEvent(id string, event any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.broadcastId = append(c.broadcastId, id)
	c.broadcasts = append(c.broadcasts, event)

	return nil
}

func (c *fakeContainer) ClosedCallbackChannel() chan func() {
	ch := make(chan func())
	close(ch)

	return ch
}

func (c *fakeContainer) broadcastCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.broadcasts)
}

// onPluginGoroutine runs f where the plugin's own code does, and waits for it.
func (c *fakeContainer) onPluginGoroutine(f func()) {
	done := make(chan struct{})
	c.work <- func() { f(); close(done) }
	<-done
}

// fixture is a started plugin whose time moves only when the test says so.
type fixture struct {
	container *fakeContainer
	plugin    *Plugin
	conf      *config.Clock
	ticks     chan time.Time
	waiting   chan struct{}

	// exited is whether the driver has finished. It takes a moment to once told to, so that what only returns
	// after it has finished can be told from what doesn't wait.
	exited atomic.Bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		container: newFakeContainer(),
		conf:      config.NewClock(),
		ticks:     make(chan time.Time),
		waiting:   make(chan struct{}, 16),
	}

	var mu sync.Mutex
	current := time.Date(2026, 3, 10, 10, 15, 20, 0, time.UTC)

	f.plugin = NewPlugin(f.conf)
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

	f.container.onPluginGoroutine(func() {
		f.plugin.Init(f.container)
		f.plugin.Start()
	})

	t.Cleanup(f.container.close)

	return f
}

// tick moves time on to the start of the given minute of the day, and waits for the driver to have taken it in.
func (f *fixture) tick(hour, minute int) {
	<-f.waiting
	f.ticks <- time.Date(2026, 3, 10, hour, minute, 0, 0, time.UTC)
}

// clockFromServer is the config's clock as the assembly gets it: the server runs the listeners, the first of which is
// the config's own and keeps the stub.
func (f *fixture) clockFromServer(t *testing.T) *config.Clock {
	t.Helper()

	f.container.mu.Lock()
	batches := f.container.server
	f.container.mu.Unlock()

	if len(batches) != 1 {
		t.Fatalf("the server was asked to run %d batches of listeners, want 1", len(batches))
	}

	for _, invocation := range batches[0] {
		invocation()
	}

	if _, err := f.conf.Running(); err != nil {
		t.Fatalf("the config's clock wasn't given the stub: %v", err)
	}

	return f.conf
}

func TestPlugin_RegistersTheClockEntityAndOnlyThenHandsOverItsStub(t *testing.T) {
	f := newFixture(t)

	if !reflect.DeepEqual(f.container.registered, []reflect.Type{spiclock.ClockType}) {
		t.Fatalf("registered %v", f.container.registered)
	}

	if _, err := f.conf.Running(); err == nil {
		t.Error("the config had a clock before the server handed it over")
	}

	if err := f.conf.Start(); err == nil {
		t.Error("the config started a clock it hadn't been given")
	}

	f.clockFromServer(t)
}

func TestPlugin_ListenersAddedBeforeStartingAreCalledWithTheStubAndTheConfigKeepsItFirst(t *testing.T) {
	container := newFakeContainer()
	defer container.close()

	conf := config.NewClock()

	var running []bool

	conf.AddOnEntityStubAvailableListener(func(event spiclock.ClockEntityStubAvailableEvent) {
		// By now the config can already use the clock, which is what lets this listener be the one that starts it.
		isRunning, err := conf.Running()
		if err != nil {
			t.Errorf("the config hadn't been given the stub by the time the assembly's listener was called: %v", err)
		}

		running = append(running, isRunning)

		if event.Id != "clock-entity-id" || event.EntityType != spiclock.ClockType || event.EntityStub == nil {
			t.Errorf("event: %+v", event)
		}
	})

	plugin := NewPlugin(conf)
	container.onPluginGoroutine(func() { plugin.Init(container); plugin.Start() })

	for _, invocation := range container.server[0] {
		invocation()
	}

	if !reflect.DeepEqual(running, []bool{false}) {
		t.Errorf("listener calls: %v", running)
	}
}

func TestPlugin_ItDoesNotRunUntilItIsStarted(t *testing.T) {
	f := newFixture(t)
	clock := f.clockFromServer(t)

	select {
	case <-f.waiting:
		t.Fatal("the driver is running although nobody started the clock")
	case <-time.After(50 * time.Millisecond):
	}

	if running, err := clock.Running(); err != nil || running {
		t.Errorf("running: %v, %v", running, err)
	}
}

func TestPlugin_StartedItBroadcastsFromTheClockEntityAndStoppedItDoesNot(t *testing.T) {
	f := newFixture(t)
	clock := f.clockFromServer(t)

	if err := clock.Start(); err != nil {
		t.Fatal(err)
	}

	if running, _ := clock.Running(); !running {
		t.Error("not running after Start")
	}

	// Starting it again changes nothing.
	if err := clock.Start(); err != nil {
		t.Fatal(err)
	}

	f.tick(10, 59)
	f.tick(11, 0)
	<-f.waiting // the driver has taken in the last tick when it's waiting for the next

	f.container.mu.Lock()
	got := append([]any(nil), f.container.broadcasts...)
	ids := append([]string(nil), f.container.broadcastId...)
	f.container.mu.Unlock()

	var kinds []string
	for _, event := range got {
		kinds = append(kinds, reflect.TypeOf(event).Name())
	}

	want := []string{"MinuteChangedEvent", "HourChangedEvent", "MinuteChangedEvent"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("broadcast %v, want %v", kinds, want)
	}

	for _, id := range ids {
		if id != "clock-entity-id" {
			t.Errorf("an event was broadcast from %q", id)
		}
	}

	if err := clock.Stop(); err != nil {
		t.Fatal(err)
	}

	if running, _ := clock.Running(); running {
		t.Error("still running after Stop")
	}

	if !f.exited.Load() {
		t.Error("Stop returned before the driver had finished")
	}

	before := f.container.broadcastCount()

	select {
	case f.ticks <- time.Date(2026, 3, 10, 11, 1, 0, 0, time.UTC):
		t.Fatal("a stopped clock is still waiting for time to pass")
	case <-time.After(50 * time.Millisecond):
	}

	if f.container.broadcastCount() != before {
		t.Error("a stopped clock broadcast something")
	}

	// And it can be started again.
	if err := clock.Start(); err != nil {
		t.Fatal(err)
	}

	f.tick(11, 2)
	<-f.waiting

	if f.container.broadcastCount() != before+1 {
		t.Errorf("a restarted clock broadcast %d events, want 1", f.container.broadcastCount()-before)
	}
}

func TestPlugin_StoppingTheServerStopsTheClockAndRemovesIt(t *testing.T) {
	f := newFixture(t)
	clock := f.clockFromServer(t)

	if err := clock.Start(); err != nil {
		t.Fatal(err)
	}

	<-f.waiting

	var done chan func()

	f.container.onPluginGoroutine(func() { done = f.plugin.Stop() })

	// The channel closes once the driver has finished.
	if _, open := <-done; open {
		t.Fatal("received a callback, wanted the channel closed")
	}

	if !f.exited.Load() {
		t.Error("the channel was closed before the driver had finished")
	}

	if !reflect.DeepEqual(f.container.deregisters, []string{"clock-entity-id"}) {
		t.Errorf("deregistered %v", f.container.deregisters)
	}

	if err := clock.Start(); err == nil {
		t.Error("a clock whose server has stopped was started")
	}
}

func TestPlugin_StoppingTheServerWithTheClockNotRunningNeedsNothingWaitedFor(t *testing.T) {
	f := newFixture(t)

	var done chan func()

	f.container.onPluginGoroutine(func() { done = f.plugin.Stop() })

	if _, open := <-done; open {
		t.Fatal("received a callback, wanted the channel closed")
	}
}
