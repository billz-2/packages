package test

import (
	"context"
	"testing"

	usereventlog "github.com/billz-2/packages/pkg/user_event_log"
	mock_usereventlog "github.com/billz-2/packages/pkg/user_event_log/mock"
	"github.com/golang/mock/gomock"
)

func TestUserEventLog_PushUserLog(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKafka := mock_usereventlog.NewMockKafka(ctrl)
	userEventLog := usereventlog.NewUserEventLogHandler(mockKafka)

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

		getData := func(ids []interface{}) map[string]map[string]interface{} {
			return map[string]map[string]interface{}{}
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

		getData := func(ids []interface{}) map[string]map[string]interface{} {
			return map[string]map[string]interface{}{
				"parent1": {
					"id1": map[string]interface{}{"name": "Test 1"},
					"id2": map[string]interface{}{"name": "Test 2"},
				},
			}
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

		getData := func(ids []interface{}) map[string]map[string]interface{} {
			data := map[string]map[string]interface{}{
				"parent1": {},
			}

			for _, id := range ids {
				data["parent1"][id.(string)] = map[string]interface{}{"name": "Test " + id.(string)}
			}

			return data
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

		getData := func(ids []interface{}) map[string]map[string]interface{} {
			return map[string]map[string]interface{}{}
		}

		// No expectations for mockKafka as no events should be pushed

		userEventLog.PushUserLog(context.Background(), template, getData)
	})
}
