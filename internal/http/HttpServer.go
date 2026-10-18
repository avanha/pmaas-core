package http

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand"
	"net/http"

	"github.com/avanha/pmaas-core/internal/plugins"
)

var XsrfValidationStatus = "xsrf-validation-status"

type HttpServer struct {
	log            *slog.Logger
	mux            *http.ServeMux
	port           int
	serverInstance *http.Server
	runDoneCh      chan error
	getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	rootHandler    http.HandlerFunc
}

func NewHttpServer(log *slog.Logger, port int) *HttpServer {
	httpServer := &HttpServer{log: log, mux: http.NewServeMux(), port: port}
	httpServer.mux.HandleFunc("/hello", httpServer.helloHandler)
	// "/{$}" matches only the exact root path, not every otherwise-unmatched request (which is
	// what a plain "/" pattern would do) - see SetRootHandler.
	httpServer.mux.HandleFunc("/{$}", httpServer.handleRoot)
	//serveMux.HandleFunc("/plugin", listPlugins)
	return httpServer
}

// SetRootHandler overrides the server's default root ("/") page with handler. Must be called
// before Start. If never called, "/" serves defaultRootHandler instead.
func (hs *HttpServer) SetRootHandler(handler http.HandlerFunc) {
	hs.rootHandler = handler
}

func (hs *HttpServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	if hs.rootHandler != nil {
		hs.rootHandler(w, r)
		return
	}

	hs.defaultRootHandler(w, r)
}

func (hs *HttpServer) RegisterPluginHandlers(plugins []*plugins.PluginWrapper) {
	for _, plugin := range plugins {
		hs.log.Debug("registering plugin handlers", "plugin", plugin.ShortName(), "config", plugin.Config)

		if plugin.StaticContentDir != "" {
			hs.configurePluginStaticContentDir(plugin, hs.mux)
		}

		for _, httpRegistration := range plugin.HttpHandlers {
			handler := httpRegistration.HandlerFunc

			if httpRegistration.RequiresXsrfValidation {
				handler = xsrfRequiredMiddleware(hs.log, handler)
			}

			if httpRegistration.SupportsXsrfValidation || httpRegistration.RequiresXsrfValidation {
				handler = xsrfMiddleware(hs.log, handler)
			}

			hs.mux.HandleFunc(httpRegistration.Pattern, handler)
		}
	}
}

// SetTLSCertificateProvider configures the server to terminate TLS, using getCertificate (Go's own
// tls.Config.GetCertificate shape) to obtain a certificate for every handshake. Must be called before
// Start. If never called, Start serves plain HTTP, exactly as before this method existed.
func (hs *HttpServer) SetTLSCertificateProvider(getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)) {
	hs.getCertificate = getCertificate
}

func (hs *HttpServer) Start() error {
	hs.serverInstance = &http.Server{
		Addr:    fmt.Sprintf(":%d", hs.port),
		Handler: hs.mux,
	}

	if hs.getCertificate != nil {
		hs.serverInstance.TLSConfig = &tls.Config{GetCertificate: hs.getCertificate}
	}

	doneCh := make(chan error)
	hs.runDoneCh = doneCh
	go func() { run(hs.log, hs.serverInstance, doneCh) }()

	return nil
}

func (hs *HttpServer) Stop(ctx context.Context) error {
	if hs.serverInstance == nil {
		return nil
	}

	serverInstance := hs.serverInstance
	hs.serverInstance = nil

	hs.log.Info("shutdown started")
	var err = serverInstance.Shutdown(ctx)

	if err == nil {
		hs.log.Info("shutdown complete")
	} else {
		hs.log.Error("shutdown completed with error", "error", err)
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("error stopping HttpServer, context done signal received while waiting for termination: %v", ctx.Err())
	case err := <-hs.runDoneCh:
		if err != nil {
			hs.log.Error("terminated with error", "error", err)
		}
		return nil
	}
}

func (hs *HttpServer) configurePluginStaticContentDir(plugin *plugins.PluginWrapper, serveMux *http.ServeMux) {
	pluginPath := plugin.AssetFullPath("")
	pluginContentFS, staticContentDir := plugin.ContentFs()

	if pluginContentFS == nil {
		hs.log.Warn("unable to serve static content, the plugin did not provide an fs.FS instance", "path", pluginPath)
		return
	}

	pluginContentReadDirFs, ok := pluginContentFS.(fs.ReadDirFS)

	if !ok {
		hs.log.Warn("unable to serve static content, the fs.FS instance the plugin provided does not implement fs.ReadDirFS", "path", pluginPath)
		return
	}

	_, err := pluginContentReadDirFs.ReadDir(plugin.StaticContentDir)

	if err != nil {
		hs.log.Warn("unable to serve static content", "path", pluginPath, "from", staticContentDir, "error", err)
		return
	}

	pluginStaticContentFS, err := fs.Sub(pluginContentFS, plugin.StaticContentDir)

	if err != nil {
		hs.log.Warn("unable to serve static content", "path", pluginPath, "from", staticContentDir, "error", err)
		return
	}

	hs.log.Debug("serving static content", "path", pluginPath, "from", staticContentDir+"/"+plugin.StaticContentDir)
	serveMux.Handle(pluginPath,
		http.StripPrefix(
			pluginPath,
			http.FileServer(dirWithLoggerFileSystem{log: hs.log, delegate: http.FS(pluginStaticContentFS)})))
}

func run(log *slog.Logger, httpServer *http.Server, doneCh chan error) {
	log.Debug("run start")
	defer func() { close(doneCh) }()

	var err error
	var functionName string

	if httpServer.TLSConfig == nil {
		functionName = "ListenAndServe"
		err = httpServer.ListenAndServe()
	} else {
		functionName = "ListenAndServeTLS"

		// Both filenames are empty because the certificate is served entirely via
		// TLSConfig.GetCertificate (see SetTLSCertificateProvider), never from files on disk.
		err = httpServer.ListenAndServeTLS("", "")
	}

	if err == nil || errors.Is(err, http.ErrServerClosed) {
		log.Debug("run completed", "function", functionName)
		err = nil
	} else {
		log.Error("run completed with error", "function", functionName, "error", err)
		doneCh <- err
	}
}

func (hs *HttpServer) defaultRootHandler(w http.ResponseWriter, _ *http.Request) {
	_, err := io.WriteString(w, "Hello, no status provider available")
	if err != nil {
		hs.log.Warn("error writing response", "error", err)
	}
}

func (hs *HttpServer) helloHandler(w http.ResponseWriter, _ *http.Request) {
	_, err := io.WriteString(w, "Hello!\n")
	if err != nil {
		hs.log.Warn("error writing response", "error", err)
	}
}

func xsrfMiddleware(log *slog.Logger, next http.HandlerFunc) http.HandlerFunc {
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
			log.Warn("xsrf: rejecting a request, the ra header does not match the ra-src cookie",
				"method", r.Method, "path", r.URL.Path, "from", r.RemoteAddr)
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

func xsrfRequiredMiddleware(log *slog.Logger, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(XsrfValidationStatus) == nil {
			// Only reachable when the request had neither an ra-src cookie nor an ra header, i.e. the
			// browser hasn't loaded a page from this server yet (which is what sets the cookie).
			log.Warn("xsrf: rejecting a request, no ra-src cookie was sent, so it can't be validated",
				"method", r.Method, "path", r.URL.Path, "from", r.RemoteAddr)
			http.Error(w, "XSRF validation required", http.StatusBadRequest)
			return
		}

		next(w, r)
	}
}
