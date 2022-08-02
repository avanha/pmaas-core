package core

import (
  "fmt"
  "net/http"
  "pmaas.io/spi"
)

type PMAAS struct {
  plugins []spi.IPMAASPlugin
}

func NewPMAAS() *PMAAS {
  return &PMAAS{}
}

func hello(w http.ResponseWriter, req *http.Request) {
  fmt.Fprintf(w, "hello from pmaas\n")
}

func listPlugins(w http.ResponseWriter, req *http.Request) {
  fmt.Fprintf(w, "plugins:\n")
}

func (*PMAAS) ListenAndServe() {
  http.HandleFunc("/", hello)
  http.HandleFunc("/plugin", listPlugins)
  http.ListenAndServe(":8090", nil)
}
