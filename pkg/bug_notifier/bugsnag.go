package bug_notifier

import (
	"github.com/bugsnag/bugsnag-go/v2"
)

func NewBugsnag(cfg Config) *bugsnag.Notifier {
	notifier := bugsnag.New(bugsnag.Configuration{
		APIKey:       cfg.APIKey,
		ReleaseStage: cfg.ReleaseStage,
		MainContext:  cfg.MainContext,
	})

	return notifier
}
