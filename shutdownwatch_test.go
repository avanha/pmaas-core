package core

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// safeBuffer is a writer that can be written to from the watchdog's goroutine and read from the test's.
type safeBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.text.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.text.String()
}

func TestWatchShutdown_SaysNothingAboutAStepThatFinishesInTime(t *testing.T) {
	var out safeBuffer

	done := watchShutdown("plugin *esphome.plugin", 200*time.Millisecond, &out)
	time.Sleep(20 * time.Millisecond)
	done()

	// Well past when it would have fired.
	time.Sleep(400 * time.Millisecond)

	if got := out.String(); got != "" {
		t.Fatalf("expected silence, got %q", got)
	}
}

// blockedForTheDump is a goroutine that is waiting, as a hung one would be, with a name to look for.
func blockedForTheDump(release <-chan struct{}) {
	<-release
}

func TestWatchShutdown_ReportsAStepThatHangsAndWhereEveryGoroutineIs(t *testing.T) {
	release := make(chan struct{})

	defer close(release)

	go blockedForTheDump(release)

	var out safeBuffer

	done := watchShutdown("plugin *esphome.plugin", 50*time.Millisecond, &out)
	defer done()

	deadline := time.After(5 * time.Second)

	for !strings.Contains(out.String(), "end of the goroutine dump") {
		select {
		case <-deadline:
			t.Fatalf("no report: %q", out.String())
		case <-time.After(10 * time.Millisecond):
		}
	}

	report := out.String()

	for _, want := range []string{
		"plugin *esphome.plugin is still stopping after 50ms", "probably hung", "goroutine ",
		// Where the blocked goroutine is, which is what makes the report worth having.
		"blockedForTheDump",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report is missing %q:\n%s", want, report)
		}
	}

	if strings.Contains(report, "did finish") {
		t.Errorf("it said it finished, and it hasn't:\n%s", report)
	}
}

func TestWatchShutdown_SaysSoWhenAStepThatWasReportedFinishesAfterAll(t *testing.T) {
	var out safeBuffer

	done := watchShutdown("the event manager", 30*time.Millisecond, &out)

	for !strings.Contains(out.String(), "end of the goroutine dump") {
		time.Sleep(10 * time.Millisecond)
	}

	done()

	got := out.String()
	if !strings.Contains(got, "the event manager did finish stopping, after ") {
		t.Fatalf("it should say it finished:\n%s", got)
	}
}

func TestWatchShutdown_DoneCanBeCalledMoreThanOnce(t *testing.T) {
	var out safeBuffer

	done := watchShutdown("x", time.Hour, &out)
	done()
	done()

	if out.String() != "" {
		t.Fatalf("got %q", out.String())
	}
}
