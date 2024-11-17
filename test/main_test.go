package test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/billz-2/packages/pkg/bug_notifier"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/tracing"
)

var ctx context.Context

func TestMain(m *testing.M) {
	ctx = context.Background()
	logger.Log = logger.New(logger.LevelInfo, "billz_packages")

	bug_notifier.Configure(bug_notifier.Config{
		APIKey:       "set_from_env",
		ReleaseStage: "test",
		MainContext:  ctx,
		AppType:      "test",
	})

	jaegerUrl := "localhost:4317"
	tp, err := tracing.NewTraceProvider(ctx, &tracing.Config{
		ServiceName: "billz_packages",
		JaegerUrl:   jaegerUrl,
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = tp.Shutdown(ctx) }()

	exitCode := m.Run()

	// wait for all spans to be exported
	time.Sleep(time.Second * 10)
	os.Exit(exitCode)
}
