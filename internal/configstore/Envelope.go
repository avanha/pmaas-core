package configstore

import (
	"encoding/json"
	"time"
)

type Envelope struct {
	SaveTime    time.Time
	PayloadType string
	Payload     json.RawMessage
}
