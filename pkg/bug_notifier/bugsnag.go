package bug_notifier

import (
	"github.com/bugsnag/bugsnag-go/v2"
	"go.uber.org/zap/zapcore"
)

type Bugsnag interface {
	Notify(err error, rawData ...interface{}) error
}

type bugsnagClient struct {
	notifier *bugsnag.Notifier
}

func NewBugsnag(cfg Config) Bugsnag {
	notifier := bugsnag.New(bugsnag.Configuration{
		APIKey:       cfg.APIKey,
		ReleaseStage: cfg.ReleaseStage,
		MainContext:  cfg.MainContext,
		AppType:      cfg.AppType,
	})

	bugsnag.OnBeforeNotify(func(e *bugsnag.Event, c *bugsnag.Configuration) error {
		for _, data := range e.RawData {
			if interfaceSlice, ok := data.([]interface{}); ok {
				if len(interfaceSlice) == 1 {
					if fields, ok := interfaceSlice[0].([]zapcore.Field); ok {
						data := make(map[string]interface{})
						for _, field := range fields {
							data[field.Key] = field.String
							if err, ok := field.Interface.(error); ok {
								data[field.Key] = err.Error()
							}
						}
						e.MetaData.Add("data", "data", data)
					}
				}
			}
		}

		return nil
	})

	return &bugsnagClient{notifier: notifier}
}

func (b *bugsnagClient) Notify(err error, rawData ...interface{}) error {
	return b.notifier.Notify(err, rawData)
}
