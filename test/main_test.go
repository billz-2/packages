package test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/billz-2/packages/pkg/event/mock_kafka"
	usereventlog "github.com/billz-2/packages/pkg/user_event_log"

	"github.com/billz-2/packages/pkg/bug_notifier"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/tracing"
)

var (
	ctx   context.Context
	kafka mock_kafka.MockKafkaI
)

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
	tp, err := tracing.NewTraceProvider(&tracing.Config{
		ServiceName: "billz_packages",
		JaegerUrl:   jaegerUrl,
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = tp.Shutdown(ctx) }()

	kafka = mock_kafka.NewMockKafka()

	usereventlog.NewUserEventLogHandler(kafka, logger.Log)

	exitCode := m.Run()

	// wait for all spans to be exported
	time.Sleep(time.Second * 10)
	os.Exit(exitCode)
}
