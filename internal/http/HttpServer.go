package http

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/http"

	"github.com/avanha/pmaas-core/internal/plugins"
	"github.com/avanha/pmaas-spi"
)

var XsrfValidationStatus = "xsrf-validation-status"

type HttpServer struct {
	mux            *http.ServeMux
	port           int
	serverInstance *http.Server
	runDoneCh      chan error
}

func NewHttpServer(port int) *HttpServer {
	httpServer := &HttpServer{mux: http.NewServeMux(), port: port}
	httpServer.mux.HandleFunc("/hello", helloHandler)
	//serveMux.HandleFunc("/plugin", listPlugins)
	return httpServer
}

func (hs *HttpServer) RegisterPluginHandlers(plugins []*plugins.PluginWrapper) {
	for _, plugin := range plugins {
		fmt.Printf("Plugin %T config: %+v\n", plugin.Instance, plugin.Config)

		if plugin.StaticContentDir != "" {
			hs.configurePluginStaticContentDir(plugin, hs.mux)
		}

		for _, httpRegistration := range plugin.HttpHandlers {
			handler := httpRegistration.HandlerFunc

			if httpRegistration.RequiresXsrfValidation {
				handler = xsrfRequiredMiddleware(handler)
			}

			if httpRegistration.SupportsXsrfValidation || httpRegistration.RequiresXsrfValidation {
				handler = xsrfMiddleware(handler)
			}

			hs.mux.HandleFunc(httpRegistration.Pattern, handler)
		}
	}
}

func (hs *HttpServer) Start() error {
	hs.serverInstance = &http.Server{
		Addr:    fmt.Sprintf(":%d", hs.port),
		Handler: hs.mux,
	}

	doneCh := make(chan error)
	hs.runDoneCh = doneCh
	go func() { run(hs.serverInstance, doneCh) }()

	return nil
}

func (hs *HttpServer) Stop(ctx context.Context) error {
	if hs.serverInstance == nil {
		return nil
	}

	serverInstance := hs.serverInstance
	hs.serverInstance = nil

	fmt.Printf("HttpServer: Shutdown started...\n")
	var err = serverInstance.Shutdown(ctx)

	if err == nil {
		fmt.Printf("HttpServer: Shutdown complete\n")
	} else {
		fmt.Printf("HttpServer: Shutdown completed with error: %s\n", err)
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("error stopping HttpServer, context done signal received while waiting for termination: %v", ctx.Err())
	case err := <-hs.runDoneCh:
		if err != nil {
			fmt.Printf("HttpServer: Terminated with error: %v", err)
		}
		return nil
	}
}

func (hs *HttpServer) configurePluginStaticContentDir(plugin *plugins.PluginWrapper, serveMux *http.ServeMux) {
	pluginPath := spi.PluginAssetFullPath(plugin.ShortName(), "")
	pluginContentFS, staticContentDir := plugin.ContentFs()

	if pluginContentFS == nil {
		fmt.Printf("Unable to serve static content for %s, plugin did not provide an fs.FS instance\n", pluginPath)
		return
	}

	pluginContentReadDirFs, ok := pluginContentFS.(fs.ReadDirFS)

	if !ok {
		fmt.Printf("Unable to serve static content for %s, fs.FS instance provided by plugin does not "+
			"implement fs.ReadDirFS\n", pluginPath)
		return
	}

	_, err := pluginContentReadDirFs.ReadDir(plugin.StaticContentDir)

	if err != nil {
		fmt.Printf("Unable to serve %s from %s: %v\n", pluginPath, staticContentDir, err)
		return
	}

	pluginStaticContentFS, err := fs.Sub(pluginContentFS, plugin.StaticContentDir)

	if err != nil {
		fmt.Printf("Unable to serve %s from %s: %v\n", pluginPath, staticContentDir, err)
		return
	}

	fmt.Printf("Serving static content for %s from %s\n",
		pluginPath, staticContentDir+"/"+plugin.StaticContentDir)
	serveMux.Handle(pluginPath,
		http.StripPrefix(
			pluginPath,
			http.FileServer(dirWithLoggerFileSystem{delegate: http.FS(pluginStaticContentFS)})))
}

func run(httpServer *http.Server, doneCh chan error) {
	fmt.Printf("HttpServer: run() start\n")
	defer func() { close(doneCh) }()
	var err = httpServer.ListenAndServe()

	if err == nil || err == http.ErrServerClosed {
		fmt.Printf("HttpServer: run() ListenAndServe completed\n")
		err = nil
	} else {
		fmt.Printf("HttpServer: run() ListenAndServe completed with error: %s\n", err)
		doneCh <- err
	}
}

func helloHandler(w http.ResponseWriter, _ *http.Request) {
	_, err := io.WriteString(w, "Hello!\n")
	if err != nil {
		fmt.Printf("Error writing response: %v\n", err)
	}
}

func xsrfMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		currentCookie, err := r.Cookie("ra-src")
		currentRaValue := ""

		if err == nil {
			currentRaValue = currentCookie.Value
		}

		suppliedRaValue := r.Header.Get("ra")

		if suppliedRaValue == "" && currentRaValue == "" {
			// There is no ra-src cookie and no ra header, proceed
			writeRaSrc(w)
			next(w, r)
			return
		}

		if suppliedRaValue != "" && subtle.ConstantTimeCompare([]byte(suppliedRaValue), []byte(currentRaValue)) != 1 {
			// Token was supplied but does not match the current value
			http.Error(w, "XSRF validation failed", http.StatusForbidden)
			return
		}

		writeRaSrc(w)
		next(w, r.WithContext(context.WithValue(r.Context(), XsrfValidationStatus, true)))
	}
}

func writeRaSrc(w http.ResponseWriter) {
	value := fmt.Sprintf("authorization%d", rand.Int63())
	newCookie := http.Cookie{
		Name:     "ra-src",
		Value:    value,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
	}
	http.SetCookie(w, &newCookie)
}

func xsrfRequiredMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(XsrfValidationStatus) == nil {
			http.Error(w, "XSRF validation required", http.StatusBadRequest)
			return
		}

		next(w, r)
	}
}
