package translation

import "errors"

// ErrUnsupportedLang is returned when there is no column for the language in the translations table.
var ErrUnsupportedLang = errors.New("translation: unsupported lang")

var (
	// ErrEmptyServiceName is returned when the service name is not set.
	ErrEmptyServiceName = errors.New("translation: empty service name")
	// ErrNilDB is returned when no database connection is passed.
	ErrNilDB = errors.New("translation: nil db")
	// ErrNilPublisher is returned when no publisher is passed.
	ErrNilPublisher = errors.New("translation: nil publisher")
	// ErrNilLogger is returned when no logger is passed.
	ErrNilLogger = errors.New("translation: nil logger")
)

// ErrRetry wraps temporary processing errors: the message must be read again.
var ErrRetry = errors.New("translation: retryable failure")
