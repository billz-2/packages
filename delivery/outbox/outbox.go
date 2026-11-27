// Package outbox writes Kafka records to PostgreSQL in the business transaction, so that they are sent only
// if it commits and are not lost while Kafka is down; the relay sends them.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/IBM/sarama"
)

const insertColumns = 4

type Header struct {
	Key   string `json:"k"`
	Value []byte `json:"v"`
}

type Message struct {
	Topic   string
	Key     string
	Value   []byte
	Headers []Header
}

// Execer is what a write needs: a *sql.Tx, a *sql.DB, their sqlx forms, or a service's wrapper of them.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Querier is the outbox's own database, for the writes without a transaction and for Check.
type Querier interface {
	Execer
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Logger is satisfied by *slog.Logger; args are key-value pairs.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Metrics receives what the relay counts. outboxprom implements it with Prometheus.
type Metrics interface {
	Sent(topic string)
	SendFailed(topic string)
	SetAside(topic string)
	OldestRowAge(seconds float64)
	// WeakTopics is the number of topics on which acks=all can still lose a record or that could not be checked,
	// -1 when none was checked.
	WeakTopics(n int)
}

type Outbox struct {
	db      Querier
	opts    Options
	log     Logger
	metrics Metrics

	mu     sync.RWMutex
	topics map[string]topicPolicy
}

// New checks the names of opts; it does not touch the database.
func New(db Querier, opts Options) (*Outbox, error) {
	if db == nil {
		return nil, errors.New("outbox: no database")
	}
	if opts.Service == "" {
		return nil, errors.New("outbox: Options.Service is required")
	}

	opts = opts.withDefaults()
	if err := opts.checkNames(); err != nil {
		return nil, err
	}

	ob := &Outbox{db: db, opts: opts, log: opts.Logger, metrics: opts.Metrics, topics: make(map[string]topicPolicy)}
	if ob.log == nil {
		ob.log = slog.Default()
	}
	if ob.metrics == nil {
		ob.metrics = noMetrics{}
	}

	return ob, nil
}

type topicPolicy struct {
	unordered bool
}

type TopicOption func(*topicPolicy)

// Ordered promises that the records of one key reach Kafka in the order they were written: one at a time,
// each after Kafka acknowledged the one before, and a record that fails holds back the rest of its topic. It is
// the default: a topic registered without options, or never registered, is ordered too.
func Ordered() TopicOption {
	return func(p *topicPolicy) { p.unordered = false }
}

// Topic registers the delivery contract of a topic. Giving up the order will be an option of its own, so that
// no topic loses it by accident; the relay sends every topic in order.
func (o *Outbox) Topic(name string, opts ...TopicOption) {
	var p topicPolicy
	for _, opt := range opts {
		opt(&p)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	o.topics[name] = p
}

func (o *Outbox) ordered(topic string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()

	return !o.topics[topic].unordered
}

// Publish writes the records in tx, or at once on the outbox's own database when tx is nil. Records of one
// topic and key are sent in the order given.
func (o *Outbox) Publish(ctx context.Context, tx Execer, msgs ...Message) error {
	exec := tx
	if exec == nil {
		exec = o.db
	}

	query, args, err := o.insert(msgs)
	if err != nil {
		return err
	}
	if _, err = exec.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}

	return nil
}

func (o *Outbox) insert(msgs []Message) (string, []any, error) {
	var query strings.Builder
	query.WriteString(`INSERT INTO ` + quoteIdent(o.opts.Table) + ` (topic, key, payload, headers) VALUES `)
	args := make([]any, 0, insertColumns*len(msgs))

	for i, msg := range msgs {
		if msg.Topic == "" {
			return "", nil, errors.New("outbox: a message without a topic")
		}

		headers, err := encodeHeaders(msg.Headers)
		if err != nil {
			return "", nil, fmt.Errorf("outbox: headers of a %s message: %w", msg.Topic, err)
		}

		// a nil value stays NULL (lib/pq would store an empty bytea), and is sent as nil
		var payload any
		if msg.Value != nil {
			payload = msg.Value
		}

		if i > 0 {
			query.WriteString(", ")
		}
		query.WriteString("(")
		for c, arg := range []any{msg.Topic, msg.Key, payload, headers} {
			args = append(args, arg)
			if c > 0 {
				query.WriteString(", ")
			}
			query.WriteString("$" + strconv.Itoa(len(args)))
		}
		query.WriteString("::jsonb)")
	}

	return query.String(), args, nil
}

func encodeHeaders(headers []Header) (string, error) {
	if len(headers) == 0 {
		return "[]", nil
	}

	raw, err := json.Marshal(headers)

	return string(raw), err
}

func (msg Message) ProducerMessage() *sarama.ProducerMessage {
	// set also when empty: sarama hashes an empty key to one partition, a nil one to a random one
	pm := &sarama.ProducerMessage{Topic: msg.Topic, Key: sarama.StringEncoder(msg.Key)}
	if msg.Value != nil {
		pm.Value = sarama.ByteEncoder(msg.Value)
	}
	for _, header := range msg.Headers {
		pm.Headers = append(pm.Headers, sarama.RecordHeader{Key: []byte(header.Key), Value: header.Value})
	}

	return pm
}

type noMetrics struct{}

func (noMetrics) Sent(string)          {}
func (noMetrics) SendFailed(string)    {}
func (noMetrics) SetAside(string)      {}
func (noMetrics) OldestRowAge(float64) {}
func (noMetrics) WeakTopics(int)       {}
