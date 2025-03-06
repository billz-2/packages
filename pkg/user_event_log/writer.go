package usereventlog

import (
	"context"
	"time"

	"github.com/billz-2/packages/config"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/tracing"
	"go.opentelemetry.io/otel/codes"
)

type userEventLogService struct {
	kafka Kafka
}

type userEventLogWriter interface {
	pushUserEventLog(ctx context.Context, template EventLogReq, getData func([]any) map[string]map[string]any, topic string)
}

func (u *userEventLogService) pushUserEventLog(ctx context.Context, template EventLogReq, getData func([]any) map[string]map[string]any, topic string) {
	const method = "userEventLogService.pushUserEventLog"
	var err error
	ctx, span := tracing.GetSpan(ctx, method)
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, method)
			span.RecordError(err)
		}
	}()

	logger.Log.DebugWithCtx(ctx, method, logger.Any("template", template))

	for i := 0; i < len(template.IDs); i += 100 {
		templateIDs := template.IDs[i:min(i+100, len(template.IDs))]

		if len(templateIDs) > 0 {
			//convert a []T to an []any
			entityIDs := make([]any, len(templateIDs))
			for i, v := range templateIDs {
				entityIDs[i] = v
			}

			data := getData(entityIDs)

			if len(data) > 0 {
				logs := make([]EventLogModel, 0)
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
							CreatedAt:        time.Now().Format(config.DateTimeFormat),
						}
						logs = append(logs, log)
					}
				}

				resp := EventLogsResp{
					EventLogs: logs,
				}
				//create event
				event := createLogEvent(resp, template.EventActionType)
				tracing.InjectDataToSpanAndEvent(ctx, &event, span)
				err = u.kafka.Push(topic, event, template.CompanyID)
				if err != nil {
					logger.Log.ErrorWithCtx(ctx, "PushUserEventLog: error sending request to kafka", logger.Error(err))
				}
			}
		}
	}
}
