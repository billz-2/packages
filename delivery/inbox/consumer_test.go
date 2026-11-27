package inbox //nolint:testpackage // drives the claims of a fake group

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGroup behaves as sarama's group for one topic of one partition: a session claims it, a claim that
// returns ends the session, and the offsets marked in a session are committed when it is released.
type fakeGroup struct {
	mu        sync.Mutex
	committed int64 // the next offset to deliver
	log       []int64
	sessions  int
	returned  atomic.Int32 // claims that returned while their session was live
	closed    chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
	messages  chan *sarama.ConsumerMessage
	onMark    func(offset int64)
	marks     chan int64
}

func newFakeGroup() *fakeGroup {
	return &fakeGroup{closed: make(chan struct{}), marks: make(chan int64, 64)}
}

func (g *fakeGroup) awaitMark(t *testing.T) int64 {
	t.Helper()
	select {
	case o := <-g.marks:
		return o
	case <-time.After(2 * time.Second):
		t.Fatal("no message was marked")

		return -1
	}
}

// send delivers offsets to the running session's claim, from the committed offset as Kafka would.
func (g *fakeGroup) send(t *testing.T, offsets ...int64) {
	t.Helper()
	for _, o := range offsets {
		require.Eventually(t, func() bool {
			g.mu.Lock()
			defer g.mu.Unlock()

			return g.messages != nil
		}, 2*time.Second, time.Millisecond)
		g.mu.Lock()
		ch := g.messages
		g.mu.Unlock()
		ch <- &sarama.ConsumerMessage{Topic: "t", Partition: 0, Offset: o, Value: []byte(strconv.FormatInt(o, 10)),
			Headers: []*sarama.RecordHeader{{Key: []byte("ce_id"), Value: []byte(fmt.Sprint("id-", o))}}}
	}
}

// rebalance ends the running session, as a revoke would.
func (g *fakeGroup) rebalance() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
}

func (g *fakeGroup) commitLog() (int64, []int64, int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.committed, append([]int64(nil), g.log...), g.sessions
}

func (g *fakeGroup) Consume(ctx context.Context, _ []string, handler sarama.ConsumerGroupHandler) error {
	select {
	case <-g.closed:
		return sarama.ErrClosedConsumerGroup
	default:
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess := &fakeSession{ctx: sctx, group: g, marked: -1}
	claim := &fakeClaim{messages: make(chan *sarama.ConsumerMessage)}
	g.mu.Lock()
	g.cancel = cancel
	g.messages = claim.messages
	g.sessions++
	g.mu.Unlock()

	if err := handler.Setup(sess); err != nil {
		return err
	}
	claimed := make(chan struct{})
	go func() {
		defer close(claimed)
		defer cancel()
		_ = handler.ConsumeClaim(sess, claim)
		if g.closedOrDone(sctx) {
			return
		}
		g.returned.Add(1)
	}()

	select {
	case <-sctx.Done():
	case <-g.closed:
		cancel()
	}
	<-claimed
	g.mu.Lock()
	g.messages = nil
	if sess.marked >= 0 {
		g.committed = sess.marked
		g.log = append(g.log, sess.marked)
	}
	g.mu.Unlock()
	_ = handler.Cleanup(sess)

	return nil
}

func (g *fakeGroup) closedOrDone(ctx context.Context) bool {
	select {
	case <-g.closed:
		return true
	default:
		return ctx.Err() != nil
	}
}

func (g *fakeGroup) Close() error {
	g.closeOnce.Do(func() { close(g.closed) })

	return nil
}

func (g *fakeGroup) Errors() <-chan error      { return nil }
func (g *fakeGroup) Pause(map[string][]int32)  {}
func (g *fakeGroup) Resume(map[string][]int32) {}
func (g *fakeGroup) PauseAll()                 {}
func (g *fakeGroup) ResumeAll()                {}
func (g *fakeGroup) isClosed() bool            { return g.closedOrDone(context.Background()) }

type fakeSession struct {
	ctx    context.Context
	group  *fakeGroup
	marked int64
}

func (s *fakeSession) Claims() map[string][]int32               { return map[string][]int32{"t": {0}} }
func (s *fakeSession) MemberID() string                         { return "member" }
func (s *fakeSession) GenerationID() int32                      { return 1 }
func (s *fakeSession) MarkOffset(string, int32, int64, string)  {}
func (s *fakeSession) Commit()                                  {}
func (s *fakeSession) ResetOffset(string, int32, int64, string) {}
func (s *fakeSession) Context() context.Context                 { return s.ctx }

func (s *fakeSession) MarkMessage(msg *sarama.ConsumerMessage, _ string) {
	if s.group.onMark != nil {
		s.group.onMark(msg.Offset)
	}
	s.group.mu.Lock()
	s.marked = msg.Offset + 1
	s.group.mu.Unlock()
	s.group.marks <- msg.Offset
}

type fakeClaim struct {
	messages chan *sarama.ConsumerMessage
}

func (c *fakeClaim) Topic() string                            { return "t" }
func (c *fakeClaim) Partition() int32                         { return 0 }
func (c *fakeClaim) InitialOffset() int64                     { return 0 }
func (c *fakeClaim) HighWaterMarkOffset() int64               { return 0 }
func (c *fakeClaim) Messages() <-chan *sarama.ConsumerMessage { return c.messages }

func startConsumer(t *testing.T, g *fakeGroup, f Func, opts ConsumerOptions) *Consumer {
	t.Helper()
	opts.Logger = testLog
	c := NewConsumer(g, opts)
	c.Handle("t", f)
	select {
	case <-c.Run(context.Background()):
	case <-time.After(2 * time.Second):
		t.Fatal("no session")
	}
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	return c
}

type gate struct {
	started chan int64
	release chan struct{}
}

func newGate() *gate { return &gate{started: make(chan int64, 16), release: make(chan struct{})} }

func (g *gate) fn(_ context.Context, m *Message) error {
	g.started <- m.Offset
	<-g.release

	return nil
}

func (g *gate) awaitStart(t *testing.T) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the message did not start")
	}
}

func TestConsumer_MarksAMessageOnlyAfterItsFuncReturnedNil(t *testing.T) {
	g := newFakeGroup()
	gt := newGate()
	c := startConsumer(t, g, gt.fn, ConsumerOptions{})

	var markedWhileRunning atomic.Bool
	running := atomic.Bool{}
	g.onMark = func(int64) {
		if running.Load() {
			markedWhileRunning.Store(true)
		}
	}
	running.Store(true)
	g.send(t, 5)
	gt.awaitStart(t)
	time.Sleep(50 * time.Millisecond)
	running.Store(false)
	close(gt.release)

	require.NoError(t, c.Stop(context.Background()))
	committed, _, _ := g.commitLog()
	assert.Equal(t, int64(6), committed)
	assert.False(t, markedWhileRunning.Load(), "marked before its Func returned")
}

func TestConsumer_AFailingFuncRunsAgainWithoutReturningTheClaim(t *testing.T) {
	g := newFakeGroup()
	var runs atomic.Int32
	c := startConsumer(t, g, func(context.Context, *Message) error {
		if runs.Add(1) < 3 {
			return errors.New("not yet")
		}

		return nil
	}, ConsumerOptions{ResendPause: 10 * time.Millisecond})

	g.send(t, 0)
	require.Eventually(t, func() bool { return runs.Load() == 3 }, 2*time.Second, time.Millisecond)
	assert.Zero(t, g.returned.Load(), "a returned claim would end the session of every partition")
	require.NoError(t, c.Stop(context.Background()))
	committed, _, sessions := g.commitLog()
	assert.Equal(t, int64(1), committed)
	assert.Equal(t, 1, sessions)
}

func TestConsumer_APanicIsAnError(t *testing.T) {
	g := newFakeGroup()
	var runs atomic.Int32
	c := startConsumer(t, g, func(context.Context, *Message) error {
		if runs.Add(1) == 1 {
			panic("boom")
		}

		return nil
	}, ConsumerOptions{ResendPause: 10 * time.Millisecond})

	g.send(t, 0)
	require.Eventually(t, func() bool { return runs.Load() == 2 }, 2*time.Second, time.Millisecond)
	require.NoError(t, c.Stop(context.Background()))
	committed, _, _ := g.commitLog()
	assert.Equal(t, int64(1), committed)
}

func TestConsumer_ARevokedPartitionWaitsForItsMessage(t *testing.T) {
	g := newFakeGroup()
	gt := newGate()
	c := startConsumer(t, g, gt.fn, ConsumerOptions{RevokeWait: 2 * time.Second})

	g.send(t, 3)
	gt.awaitStart(t)
	g.rebalance()
	time.Sleep(50 * time.Millisecond)
	close(gt.release)

	require.Eventually(t, func() bool {
		_, log, _ := g.commitLog()

		return len(log) == 1
	}, 2*time.Second, time.Millisecond)
	_, log, _ := g.commitLog()
	assert.Equal(t, []int64{4}, log, "committed with the release of the revoked session")
	require.NoError(t, c.Stop(context.Background()))
}

func TestConsumer_AnAbandonedMessageDoesNotRunTwiceWhenItsPartitionComesBack(t *testing.T) {
	g := newFakeGroup()
	gt := newGate()
	var runs atomic.Int32
	c := startConsumer(t, g, func(ctx context.Context, m *Message) error {
		runs.Add(1)

		return gt.fn(ctx, m)
	}, ConsumerOptions{RevokeWait: 50 * time.Millisecond})

	g.send(t, 3)
	gt.awaitStart(t)
	g.rebalance()
	require.Eventually(t, func() bool {
		_, _, sessions := g.commitLog()

		return sessions == 2
	}, 2*time.Second, time.Millisecond)
	committed, _, _ := g.commitLog()
	require.Equal(t, int64(0), committed, "left running past the wait: not committed")

	g.send(t, 3)
	time.Sleep(50 * time.Millisecond)
	close(gt.release)
	assert.Equal(t, int64(3), g.awaitMark(t))
	require.NoError(t, c.Stop(context.Background()))

	assert.Equal(t, int32(1), runs.Load())
	committed, _, _ = g.commitLog()
	assert.Equal(t, int64(4), committed)
}

func TestConsumer_ASessionEndingDuringARetryLeavesTheMessageToTheNextOwner(t *testing.T) {
	g := newFakeGroup()
	var runs atomic.Int32
	c := startConsumer(t, g, func(ctx context.Context, _ *Message) error {
		runs.Add(1)
		if !pause(ctx, time.Hour) {
			return errStopped
		}

		return nil
	}, ConsumerOptions{})

	g.send(t, 0)
	require.Eventually(t, func() bool { return runs.Load() == 1 }, 2*time.Second, time.Millisecond)
	g.rebalance()
	require.Eventually(t, func() bool {
		_, _, sessions := g.commitLog()

		return sessions == 2
	}, 2*time.Second, time.Millisecond)
	g.send(t, 0)
	require.Eventually(t, func() bool { return runs.Load() == 2 }, 2*time.Second, time.Millisecond)
	require.NoError(t, c.Stop(context.Background()))
	committed, _, _ := g.commitLog()
	assert.Equal(t, int64(0), committed)
}

func TestConsumer_StopFinishesTheRunningMessageAndStartsNoOther(t *testing.T) {
	g := newFakeGroup()
	gt := newGate()
	c := startConsumer(t, g, gt.fn, ConsumerOptions{})

	g.send(t, 0)
	gt.awaitStart(t)
	stopped := make(chan error, 1)
	go func() { stopped <- c.Stop(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-stopped:
		t.Fatal("Stop did not wait for the running message")
	default:
	}
	close(gt.release)
	require.NoError(t, <-stopped)

	committed, _, _ := g.commitLog()
	assert.Equal(t, int64(1), committed)
	assert.Empty(t, gt.started, "no message started after Stop")
	assert.True(t, g.isClosed())
}

func TestConsumer_StopGivesUpAtItsBudget(t *testing.T) {
	g := newFakeGroup()
	gt := newGate()
	defer close(gt.release)
	c := startConsumer(t, g, gt.fn, ConsumerOptions{})

	g.send(t, 0)
	gt.awaitStart(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.Error(t, c.Stop(ctx))
	assert.Less(t, time.Since(start), time.Second)
	committed, _, _ := g.commitLog()
	assert.Equal(t, int64(0), committed, "a message still running is delivered again after the restart")
}

// The tests below run the consumer with the inbox on PostgreSQL.

func TestConsumer_ADeadLetterIsCommittedBeforeTheMark(t *testing.T) {
	env := testDB(t)
	g := newFakeGroup()
	var deadAtMark []int
	g.onMark = func(int64) { deadAtMark = append(deadAtMark, env.count(t, "status = 2")) }
	policy := RetryPolicy{Attempts: 3, Pauses: []time.Duration{50 * time.Millisecond}}
	var runs atomic.Int32
	f := env.in.After("t", func(ctx context.Context, m *Message) error {
		runs.Add(1)
		if string(m.Value) == "0" {
			OnRow(ctx, effect("failure response"))

			return errors.New("timeout")
		}

		return nil
	}, policy)
	c := startConsumer(t, g, f, ConsumerOptions{})

	start := time.Now()
	g.send(t, 0, 1)
	require.Eventually(t, func() bool { return runs.Load() == 4 }, 2*time.Second, time.Millisecond)
	held := time.Since(start)
	require.NoError(t, c.Stop(context.Background()))

	assert.Less(t, held, 500*time.Millisecond, "the partition is held for the pauses of the policy only")
	assert.Equal(t, []int{1, 1}, deadAtMark)
	assert.Equal(t, []string{"failure response"}, env.effects(t))
	committed, _, _ := g.commitLog()
	assert.Equal(t, int64(2), committed)
}

func TestConsumer_AMessageCutByTheShutdownDoesNotRunTwice(t *testing.T) {
	env := testDB(t)
	g := newFakeGroup()
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var runs atomic.Int32
	f := env.in.After("t", func(context.Context, *Message) error {
		runs.Add(1)
		started <- struct{}{}
		<-release

		return nil
	}, RetryPolicy{})
	c := startConsumer(t, g, f, ConsumerOptions{})

	g.send(t, 0)
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.Error(t, c.Stop(ctx), "the message runs past the consumers' budget")
	close(release)
	require.Eventually(t, func() bool { return env.count(t, "status = 0") == 1 }, 2*time.Second, time.Millisecond)
	committed, _, _ := g.commitLog()
	require.Equal(t, int64(0), committed)

	restarted := newFakeGroup()
	c2 := startConsumer(t, restarted, f, ConsumerOptions{})
	restarted.send(t, 0)
	restarted.awaitMark(t)
	require.NoError(t, c2.Stop(context.Background()))
	assert.Equal(t, int32(1), runs.Load(), "the redelivery after the restart is a duplicate")
	committed, _, _ = restarted.commitLog()
	assert.Equal(t, int64(1), committed)
}

func TestConsumer_APanicInARowWriteDoesNotRunTheHandlerAgain(t *testing.T) {
	env := testDB(t)
	g := newFakeGroup()
	var runs atomic.Int32
	f := env.in.After("t", func(ctx context.Context, _ *Message) error {
		runs.Add(1)
		OnRow(ctx, func(context.Context, *sql.Tx) error { panic("encode the response") })

		return nil
	}, RetryPolicy{})
	c := startConsumer(t, g, f, ConsumerOptions{ResendPause: 10 * time.Millisecond})

	g.send(t, 0)
	g.awaitMark(t)
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, c.Stop(context.Background()))
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, 1, env.count(t, "status = 2"))
}
