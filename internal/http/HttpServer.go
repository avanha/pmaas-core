package http

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/http"

	"github.com/avanha/pmaas-core/internal/plugins"
)

var XsrfValidationStatus = "xsrf-validation-status"

type HttpServer struct {
	mux            *http.ServeMux
	port           int
	serverInstance *http.Server
	runDoneCh      chan error
	getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	rootHandler    http.HandlerFunc
}

func NewHttpServer(port int) *HttpServer {
	httpServer := &HttpServer{mux: http.NewServeMux(), port: port}
	httpServer.mux.HandleFunc("/hello", helloHandler)
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

	defaultRootHandler(w, r)
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
	pluginPath := plugin.AssetFullPath("")
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
		fmt.Printf("HttpServer: run() %s completed\n", functionName)
		err = nil
	} else {
		fmt.Printf("HttpServer: run() ListenAndServe completed with error: %s\n", err)
		doneCh <- err
	}
}

func defaultRootHandler(w http.ResponseWriter, _ *http.Request) {
	_, err := io.WriteString(w, "Hello, no status provider available")
	if err != nil {
		fmt.Printf("Error writing response: %v\n", err)
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
