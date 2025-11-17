package usereventlog

import (
	"context"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/tracing"
	"go.opentelemetry.io/otel/codes"
)

const DateTimeFormat = "2006-01-02 15:04:05"

type userEventLogService struct {
	kafka  Kafka
	logger logger.Logger
}

func (s *userEventLogService) PushUserLog(
	ctx context.Context,
	template EventLogReq,
	getData func([]any) (data map[string]map[string]any, err error),
) (err error) {
	const method = "userEventLogService.PushUserLog"
	ctx, span := tracing.GetSpan(ctx, method)
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, method)
			span.RecordError(err)
		}
		span.End()
	}()

	s.logger.DebugWithCtx(ctx, method, logger.Any("template", template))

	for i := 0; i < len(template.IDs); i += 100 {
		templateIDs := template.IDs[i:min(i+100, len(template.IDs))]

		err = s.pushUserEventLogs(ctx, template, templateIDs, getData)
		if err != nil {
			s.logger.ErrorWithCtx(ctx, "PushUserEventLog: error sending request to kafka", logger.Error(err))
			return err
		}
	}

	return nil
}

func (s *userEventLogService) pushUserEventLogs(
	ctx context.Context,
	template EventLogReq,
	templateIDs []string,
	getData func([]any) (data map[string]map[string]any, err error),
) (err error) {
	const method = "userEventLogService.pushUserEventLogs"
	ctx, span := tracing.GetSpan(ctx, method)
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, method)
			span.RecordError(err)
		}
		span.End()
	}()

	s.logger.DebugWithCtx(ctx, method, logger.Any("template", template), logger.Any("templateIDs", templateIDs))

	if len(templateIDs) == 0 {
		return nil
	}

	entityIDs := make([]any, len(templateIDs))
	for i, v := range templateIDs {
		entityIDs[i] = v
	}

	data, err := getData(entityIDs)
	if err != nil {
		s.logger.ErrorWithCtx(ctx, "error while getting data", logger.Error(err))
		return err
	}

	if len(data) == 0 {
		return nil
	}

	resp := EventLogsResp{
		EventLogs: getLogsFromData(data, template),
	}

	e := createLogEvent(resp, template.EventActionType)
	tracing.InjectDataToSpanAndEvent(ctx, &e, span)
	err = s.kafka.Push("v1.logging_service.user_event_log.write_bulk", e, template.CompanyID)
	if err != nil {
		s.logger.ErrorWithCtx(ctx, "PushUserEventLog: error sending request to kafka", logger.Error(err))
		return err
	}

	return nil
}
