package inbox

import (
	"context"
	"time"
)

// TaskContext is the context of a task that outlives its handler, such as one an AfterCommit func starts: the
// values of msg without what Detach drops, and the end, error and deadline of process, the context the
// service cancels at its shutdown. The worker ends msg as soon as the handler returns, so a task must not
// wait on msg itself.
func TaskContext(process, msg context.Context) context.Context {
	return taskContext{signal: process, values: Detach(msg)}
}

type taskContext struct {
	signal context.Context
	values context.Context
}

func (c taskContext) Deadline() (time.Time, bool) { return c.signal.Deadline() }
func (c taskContext) Done() <-chan struct{}       { return c.signal.Done() }
func (c taskContext) Err() error                  { return c.signal.Err() }
func (c taskContext) Value(key any) any           { return c.values.Value(key) }
