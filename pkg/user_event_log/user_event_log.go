package usereventlog

import (
	"context"

	"github.com/billz-2/packages/pkg/logger"
)

//var userLog userEventLogWriter

type UserEventLog interface {
	PushUserLog(
		ctx context.Context,
		template EventLogReq,
		getData func([]any) (data map[string]map[string]any, err error),
	) error
}

func NewUserEventLogHandler(kafka Kafka, logger logger.Logger) UserEventLog {
	return &userEventLogService{
		kafka:  kafka,
		logger: logger,
	}
}
