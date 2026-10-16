package config

import (
	"errors"
	"slices"
	"sync"

	"github.com/avanha/pmaas-spi/clock"
)

var errClockStubNotAvailable = errors.New("the clock isn't available yet")

// Clock is the assembly's handle on the server's clock, which is always there but doesn't run until it's started.
// The stub is not available until the server has started, so start the clock from a listener:
//
//	conf.Clock.AddOnEntityStubAvailableListener(func(event clock.ClockEntityStubAvailableEvent) {
//		_ = event.EntityStub.Start()
//	})
//
// Plugins don't need this: they find the clock entity, and its events are broadcast to whoever registers for them.
type Clock struct {
	mu                             sync.Mutex
	entityStub                     clock.Clock
	onEntityStubAvailableListeners []func(event clock.ClockEntityStubAvailableEvent)
}

// NewClock is for the server, which makes a clock for a configuration that was made without one. An assembly gets
// its clock from NewConfig.
func NewClock() *Clock {
	c := &Clock{}
	// First, so the stub is already kept by the time any listener added by the assembly is called.
	c.AddOnEntityStubAvailableListener(c.onEntityStubAvailable)

	return c
}

// AddOnEntityStubAvailableListener adds a function to be called, on the server's goroutine, when the clock's stub
// is available.
func (c *Clock) AddOnEntityStubAvailableListener(eventListener func(event clock.ClockEntityStubAvailableEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.onEntityStubAvailableListeners = append(c.onEntityStubAvailableListeners, eventListener)
}

// OnEntityStubAvailableListeners is for the server.
func (c *Clock) OnEntityStubAvailableListeners() []func(event clock.ClockEntityStubAvailableEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.onEntityStubAvailableListeners)
}

func (c *Clock) onEntityStubAvailable(event clock.ClockEntityStubAvailableEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entityStub = event.EntityStub
}

func (c *Clock) stub() (clock.Clock, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entityStub == nil {
		return nil, errClockStubNotAvailable
	}

	return c.entityStub, nil
}

// Start has the clock begin broadcasting events. See clock.Clock.
func (c *Clock) Start() error {
	stub, err := c.stub()
	if err != nil {
		return err
	}

	return stub.Start()
}

// Stop has the clock stop broadcasting events. See clock.Clock.
func (c *Clock) Stop() error {
	stub, err := c.stub()
	if err != nil {
		return err
	}

	return stub.Stop()
}

// Running is whether the clock is started.
func (c *Clock) Running() (bool, error) {
	stub, err := c.stub()
	if err != nil {
		return false, err
	}

	return stub.Running()
}
