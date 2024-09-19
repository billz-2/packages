package bug_notifier

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pkg/errors"

	"go.uber.org/zap"
)

func TestNotify(t *testing.T) {
	n := time.Now()
	Configure(Config{
		APIKey:       "set_from_env",
		ReleaseStage: "test",
		AppType:      "test",
		MainContext:  context.TODO(),
	})

	rawData := make([]zap.Field, 0)
	rawData = append(rawData, zap.Any("key1", "value1"))
	rawData = append(rawData, zap.String("key2", "value2"))
	Notify(errors.New("test"), rawData)

	fmt.Println(time.Since(n).Milliseconds())

	time.Sleep(time.Second * 2)
}
