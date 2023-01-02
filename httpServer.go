package core

import (
	"context"
	"fmt"
	"net/http"
)

type HttpServer struct {
	mux            *http.ServeMux
	port           int
	serverInstance *http.Server
	runDoneCh      chan error
}

func NewHttpServer(mux *http.ServeMux, port int) *HttpServer {
	httpServer := &HttpServer{mux: mux, port: port}
	return httpServer
}

func (httpServer *HttpServer) Start() error {
	httpServer.serverInstance = &http.Server{
		Addr:    fmt.Sprintf(":%d", httpServer.port),
		Handler: httpServer.mux,
	}

	doneCh := make(chan error)
	httpServer.runDoneCh = doneCh
	go func() { run(httpServer.serverInstance, doneCh) }()

	return nil
}

func (httpServer *HttpServer) Stop(ctx context.Context) error {
	if httpServer.serverInstance == nil {
		return nil
	}

	serverInstance := httpServer.serverInstance
	httpServer.serverInstance = nil

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
	case err := <-httpServer.runDoneCh:
		if err != nil {
			fmt.Printf("HttpServer: Terminated with error: %v", err)
		}
		return nil
	}
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
