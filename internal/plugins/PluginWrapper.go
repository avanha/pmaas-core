package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sync"

	"pmaas.io/core/config"
	"pmaas.io/core/internal/pmaasserver"
	"pmaas.io/spi"
)

type PluginWrapper struct {
	server           pmaasserver.PmaasServer
	Config           *config.PluginConfig
	Instance         spi.IPMAASPlugin
	HttpHandlers     []HttpHandlerRegistration
	EntityRenderers  []EntityRendererRegistration
	StaticContentDir string
	ContentFS        fs.FS
	PluginType       reflect.Type
	Running          bool

	// An unbuffered channel for work to execute on the plugin runner goroutine
	ExecRequestCh chan func()

	// A channel that is closed when the plugin runner goroutine needs to stop, and doesn't want any further writes to
	// execRequestCh.  This is checked by the sender.
	ExecRequestChClosed chan bool

	// A boolean that tracks whether execRequestCh is open.  This is needed because the main pmaas Run function calls
	// stopPluginRunner multiple times. Under normal execution, when the plugin is stopped, but there's also a fall-back
	// deferred execution from the main Run function.
	ExecRequestChOpen bool

	// A WaitGroup that counts the number of senders currently trying to write to execRequestCh. The stopPluginRunner
	// function waits for this to be zero before actually closing execRequestCh.  This ensures that no writes to a
	// closed channel can take place.  All senders will have either completed their write operation, or detected that a
	// stop is in progress, via execRequestChClosed.
	ExecRequestChSendOps sync.WaitGroup

	// A channel that is closed when the plugin runner goroutine is about to complete.
	RunnerDoneCh chan error
}

func NewPluginWrapper(server pmaasserver.PmaasServer, pluginWithConfig config.PluginWithConfig) *PluginWrapper {
	return &PluginWrapper{
		server:               server,
		Config:               &pluginWithConfig.Config,
		Instance:             pluginWithConfig.Instance,
		PluginType:           pluginWithConfig.PluginType,
		HttpHandlers:         make([]HttpHandlerRegistration, 0),
		EntityRenderers:      make([]EntityRendererRegistration, 0),
		ExecRequestCh:        nil,
		ExecRequestChOpen:    false,
		ExecRequestChClosed:  nil,
		ExecRequestChSendOps: sync.WaitGroup{},
		RunnerDoneCh:         nil,
		Running:              false,
	}
}

// ExecErrorFn Executes a function that returns an error using the plugin's plugin runner goroutine.  Returns
// after execution completes, or early, if there was a problem enqueueing.
func (pwc *PluginWrapper) ExecErrorFn(target func() error) error {
	errCh := make(chan error)
	f := func() {
		defer func() { close(errCh) }()
		errCh <- target()
	}

	err := pwc.ExecInternal(f)

	if err != nil {
		close(errCh)
		return err
	}

	return <-errCh
}

// ExecVoidFn Executes a function that doesn't return anything on the plugin's plugin runner goroutine.
// Returns only after execution completes, or early, if there was a problem enqueueing.
func (pwc *PluginWrapper) ExecVoidFn(target func()) error {
	doneCh := make(chan bool)
	f := func() {
		defer func() { close(doneCh) }()
		target()
	}

	err := pwc.ExecInternal(f)

	if err != nil {
		close(doneCh)
		return err
	}

	<-doneCh
	return nil
}

// ExecInternal Enqueues the specified function to execute on the plugin's runner thread.  Returns an error
// if the function cannot be enqueued, for example, when the runner has stopped accepting requests
// as part of the shutdown process.
func (pwc *PluginWrapper) ExecInternal(target func()) error {
	var err error = nil

	// The solution here is inspired by "multiple senders one receiver" at
	// https://go101.org/article/channel-closing.html

	// Indicate that a send operation is in progress to avoid having the channel closed while it's
	// used in the select statement.
	pwc.ExecRequestChSendOps.Add(1)
	defer pwc.ExecRequestChSendOps.Done()

	// Check execRequestChClosed channel to avoid doing any extra work.
	select {
	case <-pwc.ExecRequestChClosed:
		// This means the read operation completed immediately, because either there was a value,
		// or the channel is closed.  We don't care about the value, the channel is never written
		// to, only closed.
		err = errors.New("unable to execute target function, execRequestCh is closed")
		break
	default:
		// There was no value, nor immediate return, which means the channel is probably open
		break
	}

	if err != nil {
		return err
	}

	//fmt.Printf("Attempting to send to execRequestCh\n")

	// Either enqueue the callback or return an error if execRequestChClosed.  That should
	// not happen, since closing is guarded by the execRequestChSendOps WaitGroup, but this
	// will defensively avoid a lockup or send on a closed channel.
	select {
	case <-pwc.ExecRequestChClosed:
		err = errors.New("unable to execute target function, execRequestCh is closed")
		break
	case pwc.ExecRequestCh <- target:
		break
	}

	//fmt.Printf("Completed send to execRequestCh\n")

	return err
}

func (w *PluginWrapper) PluginPath() string {
	return w.PluginType.PkgPath() + "/" + w.PluginType.Name()
}

func (w *PluginWrapper) ContentFs() (fs.FS, string) {
	if w.Config.ContentPathOverride != "" {
		// The plugin config provided a path
		contentFs := os.DirFS(w.Config.ContentPathOverride)
		return contentFs, fmt.Sprintf("os.DirFS(%s)", w.Config.ContentPathOverride)
	}

	contentPathRoot := w.server.ContentPathRoot()
	if contentPathRoot != "" {
		// The server has a configured content root.  Does it have content for this plugin?
		pluginPath := w.PluginPath()
		pluginContentDir := contentPathRoot + "/" + pluginPath
		fileInfo, err := os.Stat(pluginContentDir)

		if err == nil && fileInfo.IsDir() {
			// It does, so let's use it
			contentFs := os.DirFS(pluginContentDir)
			return contentFs, fmt.Sprintf("os.DirFS(%s)", pluginContentDir)
		}
	}

	return w.ContentFS, fmt.Sprintf("%T(providedByPlugin)", w.ContentFS)
}
