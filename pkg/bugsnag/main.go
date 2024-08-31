package error_reporter

import (
	"context"
)

type ErrorReporter interface {
	Notify(err error, rawData ...interface{}) error
}

var errorReporter ErrorReporter

func init() {
	errorReporter = Configure(Config{})
}

type Config struct {
	APIKey       string
	ReleaseStage string
	AppType      string
	MainContext  context.Context
}

func Configure(cfg Config) ErrorReporter {
	errorReporter = NewBugsnag(cfg)

	return errorReporter
}

func Notify(err error, rawData ...interface{}) error {
	return errorReporter.Notify(err, rawData...)
}
