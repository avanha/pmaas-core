package plugins

import (
	"reflect"

	"github.com/avanha/pmaas-spi"
)

type EntityRendererRegistration struct {
	EntityType      reflect.Type
	RendererFactory spi.EntityRendererFactory
}
