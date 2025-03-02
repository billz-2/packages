package user_event_log

import (
	"context"
)

var userLog userEventLogWriter

func NewUserEventLogHandler(kafka Kafka) {
	userLog = &userEventLogService{
		kafka: kafka,
	}
}

func PushUserLog(ctx context.Context, template EventLogReq, getData func([]any) map[string]map[string]any) (err error) {
	return userLog.pushUserEventLog(ctx, template, getData)
}
