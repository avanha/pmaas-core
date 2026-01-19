package http

import (
	"reflect"

	"pmaas.io/spi"
)

type EntityRendererRegistration struct {
	EntityType      reflect.Type
	RendererFactory spi.EntityRendererFactory
}
