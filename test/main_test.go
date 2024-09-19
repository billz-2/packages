package test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/billz-2/packages/pkg/bug_notifier"
	"github.com/billz-2/packages/pkg/logger"
)

var ctx context.Context

func TestMain(m *testing.M) {
	ctx = context.Background()
	logger.Log = logger.New(logger.LevelError, "billz_order_service")

	bug_notifier.Configure(bug_notifier.Config{
		APIKey:       "set_from_env",
		ReleaseStage: "test",
		MainContext:  ctx,
		AppType:      "test",
	})

	exitCode := m.Run()
	time.Sleep(time.Millisecond * 10)
	os.Exit(exitCode)
}
