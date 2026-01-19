package http

import "net/http"

type HttpHandlerRegistration struct {
	Pattern     string
	HandlerFunc http.HandlerFunc
}
