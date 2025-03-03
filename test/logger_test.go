package test

import (
	"testing"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/pkg/errors"
)

func TestLog(t *testing.T) {
	err := errors.New("test error")
	logger.Log.ErrorWithCtx(ctx, "error while requesting payme service", logger.Any("test_key", "test_value"), logger.Error(errors.Wrap(err, "wrap text message")))
}
