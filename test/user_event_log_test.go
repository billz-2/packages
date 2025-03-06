package test

import (
	usereventlog "github.com/billz-2/packages/pkg/user_event_log"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func Test_PushUserLog(t *testing.T) {
	kafka.ResetQueue()

	usereventlog.PushUserLog(ctx, usereventlog.EventLogReq{
		IDs: []string{
			uuid.NewString(),
		},
	}, func(i []interface{}) map[string]map[string]any {
		return map[string]map[string]any{
			uuid.NewString(): {
				uuid.NewString(): uuid.NewString(),
			},
		}
	}, "topic")

	events, ok := kafka.GetQueue("topic")
	require.True(t, ok)
	require.Equal(t, 1, len(events))
}
