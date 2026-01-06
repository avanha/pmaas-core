package dispatcher

import (
	"context"
	"fmt"
	"sync"
)

type Dispatcher struct {
	rwLock            sync.RWMutex
	running           bool
	callbackCh        chan func()
	callbackChWriters sync.WaitGroup
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		callbackCh: make(chan func(), 50),
		// callbackChWriters is zero-initialized and ready to use
	}
}

func (d *Dispatcher) Dispatch(callbacks []func()) error {
	d.rwLock.RLock()

	if !d.running {
		d.rwLock.RUnlock()
		return fmt.Errorf("unable to dispatch callbacks, dispatcher is not running")
	}

	d.callbackChWriters.Add(1)
	defer d.callbackChWriters.Done()

	d.rwLock.RUnlock()

	for _, callback := range callbacks {
		d.callbackCh <- callback
	}

	return nil
}

func (d *Dispatcher) Run(ctx context.Context) {
	// Mark the dispatcher as running
	d.rwLock.Lock()
	d.running = true
	d.rwLock.Unlock()

	// Execute callbacks until the context is canceled
	for run := true; run; {
		select {
		case callback := <-d.callbackCh:
			d.executeCallback(callback)
			break
		case <-ctx.Done():
			run = false
			break
		}
	}

	// Mark the dispatcher as not running and close the callback channel
	d.rwLock.Lock()
	d.running = false
	d.rwLock.Unlock()

	// Close the callback channel once there are no more active writers.
	// We're using a new Go Routine so the current one can continue consuming
	// from the channel until it's empty.
	go func() {
		d.callbackChWriters.Wait()
		close(d.callbackCh)
	}()

	// Continue processing callbacks until the channel is drained.
	for callback := range d.callbackCh {
		d.executeCallback(callback)
	}
}

func (d *Dispatcher) executeCallback(callback func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("panic in callback: %v\n", r)
		}
	}()

	callback()
}
