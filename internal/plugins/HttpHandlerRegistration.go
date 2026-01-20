package plugins

import "net/http"

type HttpHandlerRegistration struct {
	Pattern     string
	HandlerFunc http.HandlerFunc
}
