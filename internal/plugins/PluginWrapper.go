package plugins

import (
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"

	"github.com/avanha/pmaas-common/mailbox"
	"github.com/avanha/pmaas-core/config"
	"github.com/avanha/pmaas-core/internal/pmaasserver"
	"github.com/avanha/pmaas-spi"
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

	// The "mailbox" for the plugin (actor0
	mailbox *mailbox.Mailbox
}

func NewPluginWrapper(server pmaasserver.PmaasServer, pluginWithConfig config.PluginWithConfig) *PluginWrapper {
	return &PluginWrapper{
		server:          server,
		Config:          &pluginWithConfig.Config,
		Instance:        pluginWithConfig.Instance,
		PluginType:      pluginWithConfig.PluginType,
		HttpHandlers:    make([]HttpHandlerRegistration, 0),
		EntityRenderers: make([]EntityRendererRegistration, 0),
		Running:         false,
	}
}

// ExecErrorFn Executes a function that returns an error using the plugin's plugin runner goroutine.  Returns
// after execution completes, or early, if there was a problem enqueueing.
func (w *PluginWrapper) ExecErrorFn(target func() error) error {
	execError, enqueueError := w.mailbox.Exec(target)

	if enqueueError != nil {
		return enqueueError
	}

	return execError
}

// ExecVoidFn Executes a function that doesn't return anything on the plugin's plugin runner goroutine.
// Returns only after execution completes, or early, if there was a problem enqueueing.
func (w *PluginWrapper) ExecVoidFn(target func()) error {
	return w.mailbox.ExecVoidFn(target)
}

// ExecInternal Enqueues the specified function to execute on the plugin's runner thread.  Returns an error
// if the function cannot be enqueued, for example, when the runner has stopped accepting requests
// as part of the shutdown process.
func (w *PluginWrapper) ExecInternal(target func()) error {
	return w.mailbox.Send(target)
}

func (w *PluginWrapper) StartPluginRunner() {
	fmt.Printf("%T plugin runner START\n", w.Instance)
	w.mailbox = mailbox.NewMailbox()
}

func (w *PluginWrapper) StopPluginRunner() {
	if w.mailbox == nil {
		return
	}

	w.mailbox.Stop()
	fmt.Printf("%T plugin runner STOP\n", w.Instance)
}

// ShortName returns the plugin's own stable identifier, used to namespace its routes under
// "/plugins/<ShortName>/" (see spi.PluginFullPath). This is unrelated to PluginPath, which is
// derived from the plugin's Go type via reflection and used only for static content/template
// asset URLs.
func (w *PluginWrapper) ShortName() string {
	return w.Instance.ShortName()
}

func (w *PluginWrapper) RouteFullPath(relativePath string) string {
	return spi.PluginRoutePrefix + w.Instance.ShortName() + "/" + strings.TrimPrefix(relativePath, "/")
}

func (w *PluginWrapper) AssetFullPath(relativePath string) string {
	return spi.PluginAssetPrefix + w.Instance.ShortName() + "/" + strings.TrimPrefix(relativePath, "/")
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
