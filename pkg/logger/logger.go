package logger

import (
	"context"
	"time"

	"github.com/billz-2/packages/pkg/bug_notifier"
	"github.com/billz-2/packages/pkg/tracing"
	"github.com/bugsnag/bugsnag-go/v2/errors"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Field ...
type Field = zapcore.Field

var (
	// Int ..
	Int = zap.Int
	// String ...
	String = zap.String
	// Error ...
	Error = zap.Error
	// Bool ...
	Bool = zap.Bool

	// Any ...
	Any = zap.Any
)

// Logger ...
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Fatal(msg string, fields ...Field)

	DebugWithCtx(ctx context.Context, msg string, fields ...Field)
	InfoWithCtx(ctx context.Context, msg string, fields ...Field)
	WarnWithCtx(ctx context.Context, msg string, fields ...Field)
	ErrorWithCtx(ctx context.Context, msg string, fields ...Field)
	FatalWithCtx(ctx context.Context, msg string, fields ...Field)
}

type loggerImpl struct {
	zap *zap.Logger
}

var (
	customTimeFormat string
	Log              Logger
)

// New ...
func New(level LogLevel, namespace string) Logger {
	if level == "" {
		level = LevelDebug
	}

	logger := loggerImpl{
		zap: newZapLogger(level, time.RFC3339),
	}

	logger.zap = logger.zap.Named(namespace)

	zap.RedirectStdLog(logger.zap)

	if Log == nil {
		Log = &logger
	}

	return &logger
}

func (l *loggerImpl) Debug(msg string, fields ...Field) {
	l.zap.Debug(msg, fields...)
}

func (l *loggerImpl) Info(msg string, fields ...Field) {
	l.zap.Info(msg, fields...)
}

func (l *loggerImpl) Warn(msg string, fields ...Field) {
	l.zap.Warn(msg, fields...)
}

func (l *loggerImpl) Error(msg string, fields ...Field) {
	l.zap.Error(msg, fields...)
}

func (l *loggerImpl) Fatal(msg string, fields ...Field) {
	l.zap.Fatal(msg, fields...)
}

func (l *loggerImpl) DebugWithCtx(ctx context.Context, msg string, fields ...Field) {
	l.log(ctx, LevelDebug, msg, fields...)
}

func (l *loggerImpl) InfoWithCtx(ctx context.Context, msg string, fields ...Field) {
	l.log(ctx, LevelInfo, msg, fields...)
}

func (l *loggerImpl) WarnWithCtx(ctx context.Context, msg string, fields ...Field) {
	l.log(ctx, LevelWarn, msg, fields...)
}

func (l *loggerImpl) ErrorWithCtx(ctx context.Context, msg string, fields ...Field) {
	l.log(ctx, LevelError, msg, fields...)
}

func (l *loggerImpl) FatalWithCtx(ctx context.Context, msg string, fields ...Field) {
	l.log(ctx, LevelFatal, msg, fields...)
}

func (l *loggerImpl) log(ctx context.Context, level LogLevel, message string, fields ...Field) {
	if traceID := getTraceIDFromContext(ctx); traceID != "" {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	if spanID := getSpanIDFromContext(ctx); spanID != "" {
		fields = append(fields, zap.String("span_id", spanID))
	}

	switch level {
	case LevelDebug:
		l.zap.Debug(message, fields...)
	case LevelInfo:
		l.zap.Info(message, fields...)
	case LevelWarn:
		l.zap.Warn(message, fields...)
	case LevelError:
		l.zap.Error(message, fields...)
	case LevelFatal:
		l.zap.Fatal(message, fields...)
	}

	if level == LevelError || level == LevelWarn {
		fields = append(fields, zap.Any("message", message))
		for _, field := range fields {
			if err, ok := field.Interface.(error); ok {
				bug_notifier.Notify(errors.New(err, 3), fields)
			}
		}
	}
}

func getTraceIDFromContext(ctx context.Context) string {
	return tracing.GetTraceIDFromContext(ctx)
}

func getSpanIDFromContext(ctx context.Context) string {
	return tracing.GetSpanIDFromContext(ctx)
}
