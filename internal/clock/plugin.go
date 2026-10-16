package clock

import (
	"context"
	"fmt"
	"time"

	"github.com/avanha/pmaas-spi"
)

// eventSourceId is what the clock's events say they're from. The clock isn't an entity, so it isn't an entity's id,
// but a receiver that wants to know where an event came from can still tell.
const eventSourceId = "PMAAS_CLOCK"

// Plugin is the server's clock, which the server adds itself. From the moment it starts until the server stops,
// it broadcasts an event as each minute, hour, day, month and year changes.
type Plugin struct {
	container spi.IPMAASContainer

	// now and wait are what the driver is given. They're the real ones, except in tests.
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) bool

	// Owned by the plugin goroutine.
	cancel   context.CancelFunc
	finished chan struct{}
}

var _ spi.IPMAASPlugin = (*Plugin)(nil)

func NewPlugin() *Plugin {
	return &Plugin{now: time.Now, wait: waitFor}
}

func (p *Plugin) ShortName() string {
	return "clock"
}

func (p *Plugin) Init(container spi.IPMAASContainer) {
	p.container = container
}

func (p *Plugin) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})

	d := &driver{
		now:  p.now,
		wait: p.wait,
		emit: func(event any) {
			if err := p.container.BroadcastEvent(eventSourceId, event); err != nil {
				fmt.Printf("%T ERROR: unable to broadcast %T: %v\n", *p, event, err)
			}
		},
	}

	p.cancel = cancel
	p.finished = finished

	go func() {
		defer close(finished)
		d.run(ctx)
	}()
}

// Stop tells the driver to stop, and returns a channel that is closed once it has, which it does promptly. It
// doesn't wait itself, since it runs on the plugin goroutine.
func (p *Plugin) Stop() chan func() {
	if p.cancel == nil {
		return p.container.ClosedCallbackChannel()
	}

	p.cancel()
	p.cancel = nil
	finished := p.finished
	p.finished = nil

	done := make(chan func())

	go func() {
		defer close(done)
		<-finished
	}()

	return done
}
