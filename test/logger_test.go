package test

import (
	"errors"
	"testing"

	"github.com/billz-2/packages/pkg/logger"
)

func TestLog(t *testing.T) {
	err := errors.New("test error")
	logger.Log.ErrorWithCtx(ctx, "error msg", logger.Any("test_key", "test_value"), logger.Error(err))
}
