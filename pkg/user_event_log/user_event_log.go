package usereventlog

import (
	"context"
)

//var userLog userEventLogWriter

type UserEventLog interface {
	PushUserLog(
		ctx context.Context,
		template EventLogReq,
		getData func([]interface{}) map[string]map[string]interface{},
	)
}

func NewUserEventLogHandler(kafka Kafka) UserEventLog {
	return &userEventLogService{
		kafka: kafka,
	}
}
