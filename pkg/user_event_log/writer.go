package usereventlog

import (
	"context"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/tracing"
	"go.opentelemetry.io/otel/codes"
)

//userEventLog.PushUserLog()
//+trace
//+add eventID as trace

const DateTimeFormat = "2006-01-02 15:04:05"

type userEventLogService struct {
	kafka Kafka
}

func (s *userEventLogService) PushUserLog(
	ctx context.Context,
	template EventLogReq,
	getData func([]interface{}) map[string]map[string]interface{},
) {
	const method = "userEventLogService.PushUserLog"
	var err error
	ctx, span := tracing.GetSpan(ctx, method)
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, method)
			span.RecordError(err)
		}
		span.End()
	}()

	logger.Log.DebugWithCtx(ctx, method, logger.Any("template", template))

	for i := 0; i < len(template.IDs); i += 100 {
		templateIDs := template.IDs[i:min(i+100, len(template.IDs))]

		s.pushUserEventLogs(ctx, template, templateIDs, getData)
	}
}

func (s *userEventLogService) pushUserEventLogs(
	ctx context.Context,
	template EventLogReq,
	templateIDs []string,
	getData func([]interface{}) map[string]map[string]interface{},
) {
	const method = "userEventLogService.pushUserEventLogs"
	var err error
	ctx, span := tracing.GetSpan(ctx, method)
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, method)
			span.RecordError(err)
		}
		span.End()
	}()

	logger.Log.DebugWithCtx(ctx, method, logger.Any("template", template), logger.Any("templateIDs", templateIDs))

	if len(templateIDs) == 0 {
		return
	}

	//convert a []T to an []interface{}
	entityIDs := make([]interface{}, len(templateIDs))
	for i, v := range templateIDs {
		entityIDs[i] = v
	}

	data := getData(entityIDs)

	if len(data) == 0 {
		return
	}

	resp := EventLogsResp{
		EventLogs: getLogsFromData(data, template),
	}

	e := createLogEvent(resp, template.EventActionType)
	tracing.InjectDataToSpanAndEvent(ctx, &e, span)
	err = s.kafka.Push("v1.logging_service.user_event_log.write_bulk", e, template.CompanyID)
	if err != nil {
		logger.Log.ErrorWithCtx(ctx, "PushUserEventLog: error sending request to kafka", logger.Error(err))
	}
}
