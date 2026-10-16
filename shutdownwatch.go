package core

import (
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"sync/atomic"
	"time"
)

// shutdownWarnAfter is how long one step of shutting down (stopping a plugin, or a core service) may take before
// it's reported as hung.
const shutdownWarnAfter = 15 * time.Second

// watchShutdown reports a step of shutting down that is taking too long, and doesn't do anything about it. The
// server deliberately has no timeouts when it stops its plugins and its own services: they are our code, so a
// step that never finishes is a bug to find, not to wait out. But that makes a hang silent, and a server that
// won't stop leaves nothing behind to find it with, particularly when its output is thrown away.
//
// So if what isn't finished after the given time, it says what it is, and where every goroutine in the process
// is, which is nearly always enough to see what is waiting for what. It goes to out (the server's standard
// error, which is where a crash's stack trace goes too, and is quiet otherwise, so is worth keeping even when
// standard output isn't). If the step does finish after that, it says so, so a slow one can be told from a
// hung one. The returned function is to be called when the step is done.
func watchShutdown(what string, after time.Duration, out io.Writer) (done func()) {
	start := time.Now()

	var reported atomic.Bool

	timer := time.AfterFunc(after, func() {
		reported.Store(true)

		_, _ = fmt.Fprintf(out,
			"PMAAS: %s is still stopping after %v, and is probably hung. Where every goroutine is:\n", what, after)
		_ = pprof.Lookup("goroutine").WriteTo(out, 2)
		_, _ = fmt.Fprintf(out, "PMAAS: end of the goroutine dump for %s\n", what)
	})

	return func() {
		timer.Stop()

		if reported.Load() {
			_, _ = fmt.Fprintf(out, "PMAAS: %s did finish stopping, after %v\n", what, time.Since(start).Round(time.Millisecond))
		}
	}
}

// watchShutdownStep is watchShutdown with the usual time, and standard error.
func watchShutdownStep(what string) (done func()) {
	return watchShutdown(what, shutdownWarnAfter, os.Stderr)
}
