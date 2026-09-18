package mailbox

import (
	"errors"
	"sync"
	"sync/atomic"
)

// Mailbox delivers work to a single owning goroutine, safely, even with
// concurrent senders and a shutdown in progress.
type Mailbox struct {

	// An unbuffered channel for work to execute on the mailbox's owning goroutine
	reqCh chan func()

	// A channel that is closed when the mailbox goroutine needs to stop, and doesn't want any further sends.  This is
	// This is checked by the sender.
	closedCh chan struct{}

	// A WaitGroup that counts the number of senders currently trying to write to requestCh. The Stop
	// function waits for this to be zero before actually closing requestCh.  This ensures that no writes to a
	// closed channel can take place.  All senders will have either completed their write operation or detected that a
	// stop is in progress, via closedCh.
	sendOps sync.WaitGroup

	// A boolean that tracks whether reqCh is open.  This is needed because the mailbox owner may call
	// Stop multiple times.
	open atomic.Bool

	// A channel that is closed when the mailbox goroutine is terminating.
	doneCh chan struct{}
}

// NewMailbox creates and starts a Mailbox. The returned Mailbox is immediately
// ready to accept work via Send/Exec.
func NewMailbox() *Mailbox {
	m := &Mailbox{
		reqCh:    make(chan func()),
		closedCh: make(chan struct{}),
		doneCh:   make(chan struct{}),
	}

	go m.run()

	// Flip open last, once every field the receive loop and senders depend on
	// is fully constructed and the goroutine is scheduled.
	m.open.Store(true)

	return m
}

func (m *Mailbox) run() {
	defer close(m.doneCh)
	for f := range m.reqCh {
		f()
	}
}

// Send enqueues target to run on the owning goroutine. It does not wait for
// target to run — use Exec for that. Returns an error if the mailbox is
// closed or closing.
func (m *Mailbox) Send(target func()) error {
	m.sendOps.Add(1)
	defer m.sendOps.Done()

	select {
	case <-m.closedCh:
		return errors.New("mailbox closed")
	default:
	}

	select {
	case <-m.closedCh:
		return errors.New("mailbox closed")
	case m.reqCh <- target:
		return nil
	}
}

// Stop closes the mailbox for new sends, waits for any in-flight sends to
// resolve, then waits for the owning goroutine to drain and exit. Safe to
// call multiple times; only the first call does anything.
func (m *Mailbox) Stop() {
	// The solution here is inspired by "multiple senders one receiver" at
	// https://go101.org/article/channel-closing.html
	if !m.open.CompareAndSwap(true, false) {
		return
	}

	//Signal that the mailbox is closing
	close(m.closedCh)

	// Wait for any pending send operations complete.  They'll either complete
	// or bail out on the done signal.
	m.sendOps.Wait()

	// Close the channel
	close(m.reqCh)

	// Finally, wait for the runner to indicate completion
	<-m.doneCh
}

// Exec sends a target closure to the mailbox and blocks until it runs, returning its result.
func (m *Mailbox) Exec[T any](target func() T) (T, error) {
	resultCh := make(chan T, 1)
	err := m.Send(func() { resultCh <- target() })

	if err != nil {
		var zero T
		return zero, err
	}
	return <-resultCh, nil
}

// ExecVoidFn sends a target closure to the mailbox and blocks until it runs.
func (m *Mailbox) ExecVoidFn(target func()) error {
	done := make(chan struct{})
	err := m.Send(func() {
		target()
		close(done)
	})
	if err != nil {
		return err
	}
	<-done
	return nil
}
