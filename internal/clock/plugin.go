package clock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/avanha/pmaas-core/config"
	"github.com/avanha/pmaas-spi"
	spiclock "github.com/avanha/pmaas-spi/clock"
	"github.com/avanha/pmaas-spi/events"
)

const entityName = "Clock"

var errStopped = errors.New("the clock has been shut down")

// Plugin is the server's clock, which the server adds itself. It registers the clock entity, and once that is
// started runs a driver that broadcasts the clock's events.
type Plugin struct {
	config    *config.Clock
	container spi.IPMAASContainer

	// now and wait are what the driver is given. They're the real ones, except in tests.
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) bool

	// Everything below belongs to the plugin goroutine.
	entityId string
	entity   *entity
	stopped  bool
}

var _ spi.IPMAASPlugin = (*Plugin)(nil)

func NewPlugin(config *config.Clock) *Plugin {
	return &Plugin{config: config, now: time.Now, wait: waitFor}
}

func (p *Plugin) ShortName() string {
	return "clock"
}

func (p *Plugin) Init(container spi.IPMAASContainer) {
	p.container = container
	p.entity = &entity{plugin: p}
}

func (p *Plugin) Start() {
	id, err := p.container.RegisterEntity("Clock", spiclock.ClockType, entityName,
		func() (any, error) { return p.entity.getStub(), nil })
	if err != nil {
		fmt.Printf("%T ERROR: unable to register the clock: %v\n", *p, err)
		return
	}

	p.entityId = id
	p.notifyStubAvailable()
}

// notifyStubAvailable hands the stub to whoever the assembly said wanted it, on the server's goroutine.
func (p *Plugin) notifyStubAvailable() {
	listeners := p.config.OnEntityStubAvailableListeners()
	event := spiclock.ClockEntityStubAvailableEvent{
		EntityEvent: events.EntityEvent{Id: p.entityId, EntityType: spiclock.ClockType, Name: entityName},
		EntityStub:  p.entity.getStub(),
	}

	invocations := make([]func(), len(listeners))
	for i, listener := range listeners {
		invocations[i] = func() { listener(event) }
	}

	if err := p.container.EnqueueOnServerGoRoutine(invocations); err != nil {
		fmt.Printf("%T ERROR: unable to tell the assembly the clock is available: %v\n", *p, err)
	}
}

// Stop stops the clock, and returns a channel that is closed once its goroutine has finished, which it does
// promptly. Doesn't wait itself, since it runs on the plugin goroutine.
func (p *Plugin) Stop() chan func() {
	p.stopped = true
	finished := p.entity.stopRunning()

	if p.entityId != "" {
		if err := p.container.DeregisterEntity(p.entityId); err != nil {
			fmt.Printf("%T ERROR: unable to deregister the clock: %v\n", *p, err)
		}

		p.entityId = ""
	}

	if finished == nil {
		return p.container.ClosedCallbackChannel()
	}

	done := make(chan func())

	go func() {
		defer close(done)
		<-finished
	}()

	return done
}

// entity is the clock entity. Owned by the plugin goroutine.
type entity struct {
	plugin *Plugin
	stub   *stub

	cancel   context.CancelFunc
	finished chan struct{}
}

func (e *entity) running() bool {
	return e.cancel != nil
}

func (e *entity) start() error {
	p := e.plugin

	if p.stopped {
		return errStopped
	}

	if e.running() {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	entityEvent := events.EntityEvent{Id: p.entityId, EntityType: spiclock.ClockType, Name: entityName}

	d := &driver{
		now:    p.now,
		wait:   p.wait,
		entity: entityEvent,
		emit: func(event any) {
			if err := p.container.BroadcastEvent(entityEvent.Id, event); err != nil {
				fmt.Printf("%T ERROR: unable to broadcast %T: %v\n", *p, event, err)
			}
		},
	}

	e.cancel = cancel
	e.finished = finished

	go func() {
		defer close(finished)
		d.run(ctx)
	}()

	return nil
}

// stopRunning tells the driver to stop, and returns a channel that closes when it has. Returns nil if it wasn't
// running. It doesn't wait, so it can be used where waiting isn't allowed.
func (e *entity) stopRunning() chan struct{} {
	if !e.running() {
		return nil
	}

	finished := e.finished
	e.cancel()
	e.cancel = nil
	e.finished = nil

	return finished
}

func (e *entity) stop() error {
	// The driver never waits for this goroutine, so waiting for it can't deadlock. It's quick, since the driver is
	// either waiting on the context or handing an event over.
	if finished := e.stopRunning(); finished != nil {
		<-finished
	}

	return nil
}

func (e *entity) getStub() *stub {
	if e.stub == nil {
		e.stub = &stub{container: e.plugin.container, target: e}
	}

	return e.stub
}

// stub is the clock as everyone else sees it: each call runs on the plugin goroutine, where the entity lives.
type stub struct {
	container spi.IPMAASContainer
	target    *entity
}

var _ spiclock.Clock = (*stub)(nil)

func (s *stub) Start() error {
	return s.exec(func() error { return s.target.start() })
}

func (s *stub) Stop() error {
	return s.exec(func() error { return s.target.stop() })
}

func (s *stub) Running() (bool, error) {
	running, err := spi.Exec(s.container, func() bool { return s.target.running() })
	if err != nil {
		return false, fmt.Errorf("unable to ask the clock: %w", err)
	}

	return running, nil
}

func (s *stub) exec(f func() error) error {
	result, err := spi.Exec(s.container, f)
	if err != nil {
		return fmt.Errorf("unable to reach the clock: %w", err)
	}

	return result
}
