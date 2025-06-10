package usereventlog

import (
	"fmt"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/google/uuid"
	"strings"
	"time"
)

func createLogEvent(value interface{}, eventType string) cloudevents.Event {
	e := cloudevents.NewEvent()
	e.SetID(uuid.NewString())
	_ = e.SetData(cloudevents.ApplicationJSON, value)
	e.SetType(eventType)
	e.SetSource("catalog_service")

	return e
}

func parseFieldString(fields map[string]interface{}, fieldName string) string {
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

func parseFieldInt32(fields map[string]interface{}, fieldName string) int32 {
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

func parseFieldInt64(fields map[string]interface{}, fieldName string) int64 {
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

func ToStringMap(values []interface{}) []string {
	stringValues := make([]string, len(values))
	for i, v := range values {
		stringValues[i] = fmt.Sprint(v)
	}

	return stringValues
}

func getLogsFromData(data map[string]map[string]interface{}, template EventLogReq) []EventLogModel {
	var logs []EventLogModel
	for parentEntityID, parentEntity := range data {
		//map values
		for id, objData := range parentEntity {
			log := EventLogModel{
				EventData:        objData,
				EventID:          template.EventID,
				EventActionType:  template.EventActionType,
				EventSource:      template.EventSource,
				ParentObjectName: template.ParentObjectName,
				ParentObjectID:   parentEntityID,
				ObjectID:         id,
				CompanyID:        template.CompanyID,
				ObjectType:       template.ObjectType,
				UserID:           template.UserID,
				SessionID:        template.SessionID,
				CreatedAt:        time.Now().Format(DateTimeFormat),
			}
			logs = append(logs, log)
		}
	}

	return logs
}
