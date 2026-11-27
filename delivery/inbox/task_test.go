package inbox //nolint:testpackage // the message's context is built with the unexported keys of the worker

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type traceKey struct{}

func TestTaskContext_KeepsTheMessagesValuesAndEndsWithTheProcess(t *testing.T) {
	process, stop := context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
	defer stop()

	msg, endRun := context.WithCancel(context.Background())
	msg = context.WithValue(msg, traceKey{}, "trace-1")
	msg = context.WithValue(msg, txKey{}, &sql.Tx{})
	msg = context.WithValue(msg, rowKey{}, &rowWrites{})
	msg = context.WithValue(msg, afterKey{}, &afterCommits{})
	msg, cause := WithCause(msg)

	task := TaskContext(process, msg)
	endRun()

	assert.Equal(t, "trace-1", task.Value(traceKey{}))
	_, hasTx := Tx(task)
	assert.False(t, hasTx, "the transaction is over when the task runs")
	assert.False(t, OnRow(task, func(context.Context, *sql.Tx) error { return nil }), "no row writes")
	assert.False(t, AfterCommit(task, func() {}), "no AfterCommit of the message")
	SetCause(task, assert.AnError)
	assert.NoError(t, cause(), "a task's error is not the handler's cause")

	require.NoError(t, task.Err(), "the end of the message's run does not reach the task")
	want, _ := process.Deadline()
	got, ok := task.Deadline()
	assert.True(t, ok)
	assert.Equal(t, want, got)

	stop()
	select {
	case <-task.Done():
	case <-time.After(time.Second):
		t.Fatal("the end of the process does not end the task")
	}
	assert.ErrorIs(t, task.Err(), context.Canceled)
}
