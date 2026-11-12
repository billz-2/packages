package translation

import "encoding/json"

// Message is the body of the translations update event of a service.
type Message struct {
	ServiceName string          `json:"service_name"`
	Version     int64           `json:"version"`
	Ru          json.RawMessage `json:"ru"`
	En          json.RawMessage `json:"en"`
	Uz          json.RawMessage `json:"uz"`
}

// Topic returns the name of the translations update topic of a service.
func Topic(serviceName string) string {
	return "v1." + serviceName + ".translations.updated"
}
