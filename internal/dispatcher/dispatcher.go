package dispatcher

import (
	"context"
	"fmt"
	"sync"
)

type Dispatcher struct {
	rwLock     sync.RWMutex
	running    bool
	callbackCh chan func()
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		callbackCh: make(chan func(), 50),
	}
}

func (d *Dispatcher) Dispatch(callbacks []func()) error {
	d.rwLock.RLock()
	defer d.rwLock.RUnlock()

	if !d.running {
		return fmt.Errorf("unable to dispatch callbacks, dispatcher is not running")
	}

	for _, callback := range callbacks {
		// This might deadlock if the channel buffer is full, and the GoRoutine executing Run is
		// waiting to acquire the write lock.  We should perform the channel write operation without
		// the lock. However, we do need to indicate that a channel write operation is in progress,
		// so the GoRoutine executing the Run does not close the channel on us.
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
	close(d.callbackCh)
	d.rwLock.Unlock()

	// Process any already enqueued callbacks
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
