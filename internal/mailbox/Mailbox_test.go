package mailbox_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/avanha/pmaas-core/internal/mailbox"
)

func TestMailbox_ExecVoidFn(t *testing.T) {
	m := mailbox.NewMailbox()
	defer m.Stop()

	var executed atomic.Bool
	err := m.ExecVoidFn(func() {
		time.Sleep(10 * time.Millisecond)
		executed.Store(true)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !executed.Load() {
		t.Fatalf("expected function to have completed execution")
	}
}

func TestMailbox_ExecVoidFn_Closed(t *testing.T) {
	m := mailbox.NewMailbox()
	m.Stop()

	err := m.ExecVoidFn(func() {
		t.Fatalf("should not execute on closed mailbox")
	})

	if err == nil {
		t.Fatalf("expected error when executing on closed mailbox, got nil")
	}
}

func TestMailbox_Exec(t *testing.T) {
	m := mailbox.NewMailbox()
	defer m.Stop()

	res, err := m.Exec(func() int {
		return 42
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res != 42 {
		t.Fatalf("expected 42, got %d", res)
	}
}
