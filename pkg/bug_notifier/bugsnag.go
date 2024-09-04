package bug_notifier

import (
	"github.com/bugsnag/bugsnag-go/v2"
	"go.uber.org/zap/zapcore"
)

func NewBugsnag(cfg Config) *bugsnag.Notifier {
	notifier := bugsnag.New(bugsnag.Configuration{
		APIKey:       cfg.APIKey,
		ReleaseStage: cfg.ReleaseStage,
		MainContext:  cfg.MainContext,
		AppType:      cfg.AppType,
	})

	bugsnag.OnBeforeNotify(func(e *bugsnag.Event, c *bugsnag.Configuration) error {
		for _, data := range e.RawData {
			if fields, ok := data.([]zapcore.Field); ok {
				e.MetaData.Add("data", "data", fields)
			}
		}

		return nil
	})

	return notifier
}
