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
		Synchronous:  true,
	})

	bugsnag.OnBeforeNotify(func(e *bugsnag.Event, c *bugsnag.Configuration) error {
		for _, data := range e.RawData {
			if fields, ok := data.([]zapcore.Field); ok {
				e.MetaData.Add("data", "data", fields)
			}
		}

		return nil
	})

	return &bugsnagClient{notifier: notifier}
}

func (b *bugsnagClient) Notify(err error, rawData ...interface{}) error {
	return b.notifier.Notify(err, rawData)
}
