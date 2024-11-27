package test

import (
	"errors"
	"testing"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/tracing"
	"go.opentelemetry.io/otel/codes"
)

func TestTrace(t *testing.T) {
	var err error
	method := "TestTrace"
	ctx, span := tracing.GetSpan(ctx, method, "some test request")
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, method+" error")
			span.RecordError(err)
		}
		span.End()
	}()
	logger.Log.InfoWithCtx(ctx, method+" request")
	err = errors.New("test error")
}
