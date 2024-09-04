package bug_notifier

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNotify(t *testing.T) {
	n := time.Now()
	Configure(Config{
		APIKey:       "dce81c01be2ed2143d2eef86981903c5",
		ReleaseStage: "test",
		AppType:      "test",
		MainContext:  context.TODO(),
	})

	rawData := make([]zap.Field, 0)
	rawData = append(rawData, zap.Any("key1", "value1"))
	rawData = append(rawData, zap.String("key2", "value2"))
	Notify(errors.New("test"), rawData)

	fmt.Println(time.Since(n).Milliseconds())

}
