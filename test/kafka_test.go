package test

import (
	"context"
	"github.com/billz-2/packages/pkg/event"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestKafka(t *testing.T) {
	kafka.AddConsumer("topic", func(ctx2 context.Context, e cloudevents.Event) event.Response {
		return event.Response{}
	})

	kafka.AddPublisher("topic")

	defer func() {
		err := kafka.Shutdown()
		require.NoError(t, err)
	}()

	e, err := event.CreateEvent("topic", "source", event.Response{})
	require.NoError(t, err)

	err = kafka.Push("topic", e)
	require.NoError(t, err)

	err = kafka.Push("topic1", e)
	require.Error(t, err)

}
