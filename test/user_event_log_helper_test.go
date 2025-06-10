package test

import (
	"testing"
	"time"

	usereventlog "github.com/billz-2/packages/pkg/user_event_log"
	"github.com/stretchr/testify/assert"
)

func TestToStringMap(t *testing.T) {
	t.Run("Empty Slice", func(t *testing.T) {
		values := []interface{}{}
		result := usereventlog.ToStringMap(values)
		assert.Empty(t, result)
	})

	t.Run("String Values", func(t *testing.T) {
		values := []interface{}{"test1", "test2", "test3"}
		result := usereventlog.ToStringMap(values)
		assert.Equal(t, []string{"test1", "test2", "test3"}, result)
	})

	t.Run("Mixed Values", func(t *testing.T) {
		values := []interface{}{123, "test", true, 45.67}
		result := usereventlog.ToStringMap(values)
		assert.Equal(t, []string{"123", "test", "true", "45.67"}, result)
	})
}

// Additional test for the DateTimeFormat constant
func TestDateTimeFormat(t *testing.T) {
	// Test that the DateTimeFormat constant is a valid time format
	timeStr := time.Now().Format(usereventlog.DateTimeFormat)
	_, err := time.Parse(usereventlog.DateTimeFormat, timeStr)
	assert.NoError(t, err)
}
