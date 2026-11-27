package inbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IBM/sarama"
)

// ConsumerOptions configure a Consumer. Their zero values are the defaults.
type ConsumerOptions struct {
	// RevokeWait is how long a revoked partition waits for its running message, so that its offset is still
	// committed; it must stay below Consumer.Group.Rebalance.Timeout (sarama's default 60 s). Default 50 s.
	RevokeWait time.Duration
	// ResendPause is the pause before a Func that returned an error runs the message again. Default 1 s.
	ResendPause time.Duration
	Logger      Logger
}

const (
	defaultRevokeWait  = 50 * time.Second
	defaultResendPause = time.Second
	errorPause         = time.Second
	inFlightPoll       = 5 * time.Millisecond
)

// Consumer runs one sarama group for all its topics. A partition's messages run one at a time, and an
// offset is marked only after its Func returned nil. A Func that fails runs again in process: a claim
// never returns to retry, because sarama ends the whole session when one claim returns.
type Consumer struct {
	group sarama.ConsumerGroup
	opts  ConsumerOptions
	log   Logger

	mu    sync.RWMutex
	funcs map[string]Func

	base      context.Context
	runCtx    context.Context
	runCancel context.CancelFunc

	closing    context.Context
	closeFunc  context.CancelFunc
	inFlight   atomic.Int64
	startOnce  sync.Once
	stopOnce   sync.Once
	stopErr    error
	started    chan struct{}
	ready      chan struct{}
	readyOnce  sync.Once
	done       chan struct{}
	abandonMu  sync.Mutex
	abandoned  map[string]*run
	revokeWait time.Duration
}

func NewConsumer(group sarama.ConsumerGroup, opts ConsumerOptions) *Consumer {
	if opts.RevokeWait <= 0 {
		opts.RevokeWait = defaultRevokeWait
	}
	if opts.ResendPause <= 0 {
		opts.ResendPause = defaultResendPause
	}
	c := &Consumer{
		group:      group,
		opts:       opts,
		log:        opts.Logger,
		funcs:      make(map[string]Func),
		started:    make(chan struct{}),
		ready:      make(chan struct{}),
		done:       make(chan struct{}),
		abandoned:  make(map[string]*run),
		revokeWait: opts.RevokeWait,
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	c.closing, c.closeFunc = context.WithCancel(context.Background())

	return c
}

// Handle registers the Func of a topic before Run. A topic has one Func.
func (c *Consumer) Handle(topic string, f Func) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.funcs[topic]; ok {
		panic("inbox: a second Func for topic " + topic)
	}
	c.funcs[topic] = f
}

// Run joins the group with the registered topics until Stop. The Funcs run on ctx, which a rebalance does
// not cut. The channel is closed when the first session is set up.
func (c *Consumer) Run(ctx context.Context) <-chan struct{} {
	c.startOnce.Do(func() {
		c.base = ctx
		c.runCtx, c.runCancel = context.WithCancel(ctx)
		close(c.started)
		go c.loop()
	})

	return c.ready
}

func (c *Consumer) loop() {
	defer close(c.done)

	c.mu.RLock()
	topics := make([]string, 0, len(c.funcs))
	for topic := range c.funcs {
		topics = append(topics, topic)
	}
	c.mu.RUnlock()

	for {
		err := c.group.Consume(c.runCtx, topics, (*groupHandler)(c))
		if c.runCtx.Err() != nil || errors.Is(err, sarama.ErrClosedConsumerGroup) {
			return
		}
		if c.closing.Err() != nil {
			// joining again on the way out would rebalance the whole group once more
			<-c.runCtx.Done()

			return
		}
		if err != nil {
			c.log.Error("inbox: the consumer group session failed", "error", err)
			select {
			case <-c.runCtx.Done():
				return
			case <-time.After(errorPause):
			}
		}
	}
}

// Stop stops handing out messages, waits for the running ones until ctx ends, ends the session (which commits)
// and leaves the group. It runs once. A message still running past ctx is delivered again after the restart.
func (c *Consumer) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() {
		c.closeFunc()
		stopped := make(chan error, 1)
		go func() { stopped <- c.stop(ctx) }()

		select {
		case err := <-stopped:
			c.stopErr = err
		case <-ctx.Done():
			c.stopErr = errors.New("inbox: the consumer did not stop in time: a message still running is " +
				"delivered again after the restart")
		}
	})

	return c.stopErr
}

func (c *Consumer) stop(ctx context.Context) error {
	poll := time.NewTicker(inFlightPoll)
	for c.inFlight.Load() > 0 && ctx.Err() == nil {
		select {
		case <-poll.C:
		case <-ctx.Done():
		}
	}
	poll.Stop()

	select {
	case <-c.started:
		c.runCancel()
		<-c.done
	default:
	}

	if err := c.group.Close(); err != nil {
		return fmt.Errorf("inbox: could not close the consumer group: %w", err)
	}

	return nil
}

type groupHandler Consumer

func (h *groupHandler) Setup(sarama.ConsumerGroupSession) error {
	h.readyOnce.Do(func() { close(h.ready) })

	return nil
}

func (h *groupHandler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *groupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	c := (*Consumer)(h)
	c.mu.RLock()
	f := c.funcs[claim.Topic()]
	c.mu.RUnlock()

	revoke := &revokeDeadline{wait: c.revokeWait}
	for {
		select {
		case km, ok := <-claim.Messages():
			if !ok || ended(sess) {
				return nil
			}
			// counted before the check, so that Stop either sees it running or this claim sees Stop
			c.inFlight.Add(1)
			if c.closing.Err() != nil {
				c.inFlight.Add(-1)

				return c.waitForEnd(sess)
			}
			processed := c.process(sess, revoke, f, km)
			c.inFlight.Add(-1)
			if !processed {
				if ended(sess) {
					return nil
				}

				return c.waitForEnd(sess)
			}
		case <-sess.Context().Done():
			return nil
		case <-c.closing.Done():
			return c.waitForEnd(sess)
		}
	}
}

func ended(sess sarama.ConsumerGroupSession) bool {
	select {
	case <-sess.Context().Done():
		return true
	default:
		return false
	}
}

// waitForEnd keeps a stopping claim open: sarama ends the session as soon as one claim returns, and the
// session must outlive the running messages of the other partitions so that their offsets are committed.
func (c *Consumer) waitForEnd(sess sarama.ConsumerGroupSession) error {
	<-sess.Context().Done()

	return nil
}

type run struct {
	done chan struct{}
	err  error
}

func (c *Consumer) start(f Func, km *sarama.ConsumerMessage, stop <-chan struct{}) *run {
	r := &run{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		defer func() {
			if v := recover(); v != nil {
				r.err = fmt.Errorf("inbox: the Func of %s panicked: %v", km.Topic, v)
			}
		}()
		if f == nil {
			r.err = errors.New("inbox: no Func for topic " + km.Topic)

			return
		}
		r.err = f(withStop(c.base, stop), FromKafka(km))
	}()

	return r
}

// process runs a message until its Func returns nil, and marks it. It returns false when the session or the
// consumer ended first: the message is then left unmarked for the next owner.
func (c *Consumer) process(sess sarama.ConsumerGroupSession, revoke *revokeDeadline, f Func,
	km *sarama.ConsumerMessage,
) bool {
	if done, ok := c.awaitAbandoned(sess, km); done {
		return ok
	}

	stopCtx, stopCancel := context.WithCancel(sess.Context())
	defer stopCancel()
	defer context.AfterFunc(c.closing, stopCancel)()

	for {
		r := c.start(f, km, stopCtx.Done())
		select {
		case <-r.done:
			if r.err == nil {
				sess.MarkMessage(km, "")

				return true
			}
			if stopCtx.Err() != nil || c.base.Err() != nil {
				return false
			}
			c.log.Error("inbox: a message failed, it runs again", "topic", km.Topic, "partition", km.Partition,
				"offset", km.Offset, "error", r.err)
			select {
			case <-stopCtx.Done():
				return false
			case <-time.After(c.opts.ResendPause):
			}
		case <-sess.Context().Done():
			return c.finishRevoked(sess, revoke, km, r)
		case <-c.closing.Done():
			return c.finishStopping(sess, km, r)
		}
	}
}

func (c *Consumer) finishStopping(sess sarama.ConsumerGroupSession, km *sarama.ConsumerMessage, r *run) bool {
	select {
	case <-r.done:
		if r.err == nil {
			sess.MarkMessage(km, "")

			return true
		}
	case <-sess.Context().Done():
	}

	return false
}

type revokeDeadline struct {
	wait time.Duration
	at   time.Time
}

// time is counted once per claim: the messages of a revoked partition share one wait.
func (d *revokeDeadline) time() time.Time {
	if d.at.IsZero() {
		d.at = time.Now().Add(d.wait)
	}

	return d.at
}

// finishRevoked waits for the message of a revoked partition so that its offset is committed with the
// release. One that runs past the wait is left running; if the partition comes back to this pod, it waits
// for that run instead of starting a second one.
func (c *Consumer) finishRevoked(sess sarama.ConsumerGroupSession, revoke *revokeDeadline,
	km *sarama.ConsumerMessage, r *run,
) bool {
	timer := time.NewTimer(time.Until(revoke.time()))
	defer timer.Stop()

	select {
	case <-r.done:
		if r.err == nil {
			sess.MarkMessage(km, "")

			return true
		}
	case <-timer.C:
		c.abandon(km, r)
	case <-c.closing.Done():
	}

	return false
}

func messageKey(km *sarama.ConsumerMessage) string {
	return km.Topic + "/" + strconv.FormatInt(int64(km.Partition), 10) + "/" + strconv.FormatInt(km.Offset, 10)
}

func (c *Consumer) abandon(km *sarama.ConsumerMessage, r *run) {
	key := messageKey(km)
	c.abandonMu.Lock()
	c.abandoned[key] = r
	c.abandonMu.Unlock()

	// forgotten some time after it ended, in case the partition went to another pod
	go func() {
		<-r.done
		time.AfterFunc(c.revokeWait, func() {
			c.abandonMu.Lock()
			defer c.abandonMu.Unlock()
			if c.abandoned[key] == r {
				delete(c.abandoned, key)
			}
		})
	}()
}

// awaitAbandoned waits for an earlier run of the message that its revoked partition left behind. done is
// false when the message has to run anew.
func (c *Consumer) awaitAbandoned(sess sarama.ConsumerGroupSession, km *sarama.ConsumerMessage) (done, ok bool) {
	key := messageKey(km)
	c.abandonMu.Lock()
	r, found := c.abandoned[key]
	delete(c.abandoned, key)
	c.abandonMu.Unlock()
	if !found {
		return false, false
	}

	select {
	case <-r.done:
		if r.err != nil {
			return false, false
		}
		sess.MarkMessage(km, "")

		return true, true
	case <-sess.Context().Done():
		c.abandon(km, r)

		return true, false
	case <-c.closing.Done():
		return true, false
	}
}
