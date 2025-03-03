package event

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/IBM/sarama"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/google/uuid"
)

func MessageToEvent(message *sarama.ConsumerMessage) (cloudevents.Event, error) {
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

	var m map[string]interface{}
	err := json.Unmarshal(message.Value, &m)
	if err != nil {
		return event, err
	}

	err = event.SetData(cloudevents.ApplicationJSON, m)
	if err != nil {
		return event, err
	}

	return event, nil
}

func CreateEvent(source string, value interface{}) cloudevents.Event {
	event := cloudevents.NewEvent()
	id, _ := uuid.NewRandom()
	event.SetID(id.String())
	_ = event.SetData(cloudevents.ApplicationJSON, value)
	event.SetType("create")
	event.SetSource(source)

	return event
}
