package usereventlog

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	cloudevents "github.com/cloudevents/sdk-go/v2"
)

func createLogEvent(value any, eventType string) cloudevents.Event {
	event := cloudevents.NewEvent()
	event.SetID(uuid.NewString())
	event.SetData(cloudevents.ApplicationJSON, value)
	event.SetType(eventType)
	event.SetSource("catalog_service")

	return event
}

func parseFieldString(fields map[string]any, fieldName string) string {
	var resp string
	for key, value := range fields {
		if strings.HasPrefix(strings.ToUpper(key), strings.ToUpper(fieldName)) {
			switch v := value.(type) {
			case string:
				resp = v
			}
		}
	}

	return resp
}

func parseFieldInt32(fields map[string]any, fieldName string) int32 {
	var resp int32
	for key, value := range fields {
		if strings.HasPrefix(strings.ToUpper(key), strings.ToUpper(fieldName)) {
			switch v := value.(type) {
			case int32:
				resp = v
			}
		}
	}

	return resp
}

func parseFieldInt64(fields map[string]any, fieldName string) int64 {
	var resp int64
	for key, value := range fields {
		if strings.HasPrefix(strings.ToUpper(key), strings.ToUpper(fieldName)) {
			switch v := value.(type) {
			case int64:
				resp = v
			}
		}
	}

	return resp
}

func ToStringMap(values []any) []string {
	stringValues := make([]string, len(values))
	for i, v := range values {
		stringValues[i] = fmt.Sprint(v)
	}

	return stringValues
}
