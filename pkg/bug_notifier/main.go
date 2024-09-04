package bug_notifier

import (
	"context"
	"fmt"
)

type BugNotifier interface {
	Notify(err error, rawData ...interface{}) error
}

var bugNotifier BugNotifier

func init() {
	bugNotifier = Configure(Config{})
}

type Config struct {
	APIKey       string
	ReleaseStage string
	AppType      string
	MainContext  context.Context
}

func Configure(cfg Config) BugNotifier {
	bugNotifier = NewBugsnag(cfg)

	return bugNotifier
}

func Notify(err error, rawData ...interface{}) error {
	fmt.Printf("Bugsnag Notify data: %+v\n", rawData)
	return bugNotifier.Notify(err, rawData...)
}
