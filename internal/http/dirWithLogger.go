package http

import (
	"fmt"
	"net/http"
)

type dirWithLoggerFileSystem struct {
	delegate http.FileSystem
}

func (dwlfs dirWithLoggerFileSystem) Open(name string) (http.File, error) {
	file, err := dwlfs.delegate.Open(name)

	if err != nil {
		fmt.Printf("Error opening %s: %v\n", name, err)
	}

	return file, err
}
