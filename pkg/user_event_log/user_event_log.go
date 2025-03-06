package usereventlog

import (
	"context"
)

var userLog userEventLogWriter

func NewUserEventLogHandler(kafka Kafka) {
	userLog = &userEventLogService{
		kafka: kafka,
	}
}

func PushUserLog(ctx context.Context, template EventLogReq, getData func([]interface{}) map[string]map[string]interface{}, topic string) {
	userLog.pushUserEventLog(ctx, template, getData, topic)
}
