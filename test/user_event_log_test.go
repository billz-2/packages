package test

import (
	"context"
	"errors"
	"testing"

	"github.com/billz-2/packages/pkg/logger"
	usereventlog "github.com/billz-2/packages/pkg/user_event_log"
	mock_usereventlog "github.com/billz-2/packages/pkg/user_event_log/mock"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

func TestUserEventLog_PushUserLog(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKafka := mock_usereventlog.NewMockKafka(ctrl)
	logger := logger.New("debug", "test")
	userEventLog := usereventlog.NewUserEventLogHandler(mockKafka, logger)

	// Test case: empty IDs slice
	t.Run("Empty IDs", func(t *testing.T) {
		// No expectations for mockKafka as no events should be pushed

		template := usereventlog.EventLogReq{
			CompanyID:        "company1",
			UserID:           "user1",
			SessionID:        "session1",
			EventID:          "event1",
			EventActionType:  "create",
			EventSource:      "test",
			ParentObjectName: "parent",
			ParentObjectID:   "parent1",
			ObjectID:         "object1",
			ObjectType:       "test_object",
			IDs:              []string{},
		}

		getData := func(ids []any) (map[string]map[string]any, error) {
			return map[string]map[string]any{}, nil
		}

		// This should not panic and should not call kafka.Push
		userEventLog.PushUserLog(context.Background(), template, getData)
	})

	// Test case: with data
	t.Run("With Data", func(t *testing.T) {
		template := usereventlog.EventLogReq{
			CompanyID:        "company1",
			UserID:           "user1",
			SessionID:        "session1",
			EventID:          "event1",
			EventActionType:  "create",
			EventSource:      "test",
			ParentObjectName: "parent",
			ParentObjectID:   "parent1",
			ObjectID:         "object1",
			ObjectType:       "test_object",
			IDs:              []string{"id1", "id2"},
		}

		getData := func(ids []any) (map[string]map[string]any, error) {
			return map[string]map[string]any{
				"parent1": {
					"id1": map[string]any{"name": "Test 1"},
					"id2": map[string]any{"name": "Test 2"},
				},
			}, nil
		}

		// Expect kafka.Push to be called once with the right topic and company ID
		mockKafka.EXPECT().
			Push("v1.logging_service.user_event_log.write_bulk", gomock.Any(), "company1").
			Return(nil)

		userEventLog.PushUserLog(context.Background(), template, getData)
	})

	// Test case: with more than 100 IDs (should batch)
	t.Run("Batch Processing", func(t *testing.T) {
		// Create a template with 150 IDs
		ids := make([]string, 150)
		for i := 0; i < 150; i++ {
			ids[i] = "id" + string(rune(i))
		}

		template := usereventlog.EventLogReq{
			CompanyID:        "company1",
			UserID:           "user1",
			SessionID:        "session1",
			EventID:          "event1",
			EventActionType:  "create",
			EventSource:      "test",
			ParentObjectName: "parent",
			ParentObjectID:   "parent1",
			ObjectID:         "object1",
			ObjectType:       "test_object",
			IDs:              ids,
		}

		getData := func(ids []any) (map[string]map[string]any, error) {
			data := map[string]map[string]any{
				"parent1": {},
			}

			for _, id := range ids {
				data["parent1"][id.(string)] = map[string]any{"name": "Test " + id.(string)}
			}

			return data, nil
		}

		// Expect kafka.Push to be called twice (once for each batch)
		mockKafka.EXPECT().
			Push("v1.logging_service.user_event_log.write_bulk", gomock.Any(), "company1").
			Return(nil).
			Times(2)

		userEventLog.PushUserLog(context.Background(), template, getData)
	})

	// Test case: getData returns empty data
	t.Run("Empty Data", func(t *testing.T) {
		template := usereventlog.EventLogReq{
			CompanyID:        "company1",
			UserID:           "user1",
			SessionID:        "session1",
			EventID:          "event1",
			EventActionType:  "create",
			EventSource:      "test",
			ParentObjectName: "parent",
			ParentObjectID:   "parent1",
			ObjectID:         "object1",
			ObjectType:       "test_object",
			IDs:              []string{"id1", "id2"},
		}

		getData := func(ids []any) (map[string]map[string]any, error) {
			return map[string]map[string]any{}, nil
		}

		// No expectations for mockKafka as no events should be pushed

		userEventLog.PushUserLog(context.Background(), template, getData)
	})

	// Test case: getData returns error
	t.Run("GetData Error", func(t *testing.T) {
		template := usereventlog.EventLogReq{
			CompanyID:        "company1",
			UserID:           "user1",
			SessionID:        "session1",
			EventID:          "event1",
			EventActionType:  "create",
			EventSource:      "test",
			ParentObjectName: "parent",
			ParentObjectID:   "parent1",
			ObjectID:         "object1",
			ObjectType:       "test_object",
			IDs:              []string{"id1", "id2"},
		}

		getData := func(ids []any) (map[string]map[string]any, error) {
			return nil, errors.New("data retrieval failed")
		}

		// No expectations for mockKafka as function should return early on error

		err := userEventLog.PushUserLog(context.Background(), template, getData)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "data retrieval failed")
	})
}
