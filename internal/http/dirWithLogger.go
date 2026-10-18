package http

import (
	"log/slog"
	"net/http"
)

type dirWithLoggerFileSystem struct {
	log      *slog.Logger
	delegate http.FileSystem
}

func (dwlfs dirWithLoggerFileSystem) Open(name string) (http.File, error) {
	file, err := dwlfs.delegate.Open(name)

	if err != nil {
		dwlfs.log.Debug("unable to open static content", "name", name, "error", err)
	}

	return file, err
}
