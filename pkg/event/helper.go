package event

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/IBM/sarama"
	"github.com/google/uuid"

	cloudevents "github.com/cloudevents/sdk-go/v2"
)

func MessageToEvent(message *sarama.ConsumerMessage) cloudevents.Event {
	event := cloudevents.NewEvent()

	for _, header := range message.Headers {
		if x := string(header.Key); x == "ce_id" {
			event.SetID(string(header.Value))
		} else if x == "ce_source" {
			event.SetSource(string(header.Value))
		} else if x == "ce_type" {
			event.SetType(string(header.Value))
		} else if x == "ce_time" {
			t, _ := time.Parse("2006-01-02T15:04:05.999999999Z", string(header.Value))
			event.SetTime(t)
		} else if x == "ce_traceid" {
			event.SetExtension("traceid", string(header.Value))
		} else if x == "ce_traceparent" {
			event.SetExtension("traceparent", string(header.Value))
		} else if x == "ce_companyid" {
			event.SetExtension("companyid", string(header.Value))
		} else {
			fmt.Println("not equal: ", x)
		}
	}

	var m map[string]any
	_ = json.Unmarshal(message.Value, &m)
	_ = event.SetData(cloudevents.ApplicationJSON, m)

	return event
}

func CreateEvent(t, s string, v any) (cloudevents.Event, error) {
	event := cloudevents.NewEvent()
	id, err := uuid.NewRandom()
	if err != nil {
		return event, err
	}
	event.SetType(t)
	event.SetSource(s)
	event.SetID(id.String())
	err = event.SetData(cloudevents.ApplicationJSON, v)
	return event, err
}
