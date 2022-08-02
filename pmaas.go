package core

import (
	"fmt"
	"net/http"
	"pmaas.io/spi"
)

type Config struct {
	HttpPort int
}

func NewConfig() *Config {
	return &Config{
		8090,
	}
}

type PMAAS struct {
	config  *Config
	plugins []spi.IPMAASPlugin
}

func NewPMAAS(config *Config) *PMAAS {
	return &PMAAS{config: config}
}

func hello(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(w, "hello from pmaas\n")
}

func listPlugins(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(w, "plugins:\n")
}

func (pmaas *PMAAS) ListenAndServe() {
	http.HandleFunc("/", hello)
	http.HandleFunc("/plugin", listPlugins)
	http.ListenAndServe(fmt.Sprintf(":%d", pmaas.config.HttpPort), nil)
}
