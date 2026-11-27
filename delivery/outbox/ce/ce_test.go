package ce_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/IBM/sarama/mocks"
	"github.com/cloudevents/sdk-go/protocol/kafka_sarama/v2"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/billz-2/packages/delivery/outbox"
	"github.com/billz-2/packages/delivery/outbox/ce"
)

// written records what the outbox's INSERTs carried, and through which database.
type written struct {
	mu   sync.Mutex
	msgs []outbox.Message
	via  []string
}

func (w *written) topics() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	topics := make([]string, 0, len(w.msgs))
	for _, msg := range w.msgs {
		topics = append(topics, msg.Topic)
	}

	return topics
}

type recordingDB struct {
	name string
	w    *written
	err  error
}

func (d recordingDB) ExecContext(_ context.Context, _ string, args ...any) (sql.Result, error) {
	if d.err != nil {
		return nil, d.err
	}

	d.w.mu.Lock()
	defer d.w.mu.Unlock()
	for i := 0; i+3 < len(args); i += 4 {
		msg := outbox.Message{Topic: args[i].(string), Key: args[i+1].(string)}
		if value, ok := args[i+2].([]byte); ok {
			msg.Value = value
		}
		if err := json.Unmarshal([]byte(args[i+3].(string)), &msg.Headers); err != nil {
			return nil, err
		}
		d.w.msgs = append(d.w.msgs, msg)
		d.w.via = append(d.w.via, d.name)
	}

	return driverResult(len(args) / 4), nil
}

type driverResult int64

func (driverResult) LastInsertId() (int64, error) { return 0, errors.New("not supported") }

func (r driverResult) RowsAffected() (int64, error) { return int64(r), nil }

func (d recordingDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("not used")
}

func newPublisher(t *testing.T, opts ce.Options) (*ce.Publisher, *written) {
	t.Helper()

	w := &written{}
	ob, err := outbox.New(recordingDB{name: "pool", w: w}, outbox.Options{Service: "ce_test"})
	require.NoError(t, err)

	return ce.NewPublisher(ob, opts), w
}

func newEvent(t *testing.T, data any) cloudevents.Event {
	t.Helper()

	e := cloudevents.NewEvent()
	e.SetID("id-1")
	e.SetType("t")
	e.SetSource("test")
	require.NoError(t, e.SetData(cloudevents.ApplicationJSON, data))

	return e
}

func TestPush_UnknownTopicIsRefused(t *testing.T) {
	p, w := newPublisher(t, ce.Options{})
	require.Error(t, p.Push(context.Background(), "not.added", cloudevents.NewEvent(), "k"))
	assert.Empty(t, w.topics())

	assert.True(t, p.AddTopic("t"))
	assert.False(t, p.AddTopic("t"), "added before")
	p.AddTopic("a")
	assert.Equal(t, []string{"a", "t"}, p.Topics())
}

func TestPush_WritesToTheOutbox(t *testing.T) {
	p, w := newPublisher(t, ce.Options{PartitionKeyExtension: "partitionkey"})
	p.AddTopic("t", outbox.Ordered())

	e := newEvent(t, map[string]int{"a": 1})
	require.NoError(t, p.Push(context.Background(), "t", e, "company-1"))
	require.NoError(t, p.PushTx(context.Background(), recordingDB{name: "tx", w: w}, "t", e, ""))

	require.Len(t, w.msgs, 2)
	assert.Equal(t, "company-1", w.msgs[0].Key)
	assert.Equal(t, "pool", w.via[0])
	assert.Equal(t, "id-1", w.msgs[1].Key, "no key: the event id")
	assert.Equal(t, "tx", w.via[1])

	headers := map[string]string{}
	for _, header := range w.msgs[0].Headers {
		headers[header.Key] = string(header.Value)
	}
	assert.Equal(t, "company-1", headers["ce_partitionkey"])
}

func TestPush_ReturnsTheWriteError(t *testing.T) {
	boom := errors.New("db is down")
	ob, err := outbox.New(recordingDB{err: boom}, outbox.Options{Service: "ce_test"})
	require.NoError(t, err)
	p := ce.NewPublisher(ob, ce.Options{})
	p.AddTopic("t")

	err = p.Push(context.Background(), "t", newEvent(t, map[string]int{"a": 1}), "k")
	require.ErrorIs(t, err, boom)
	assert.False(t, ce.IsLasting(err), "a failed write can be tried again")
}

// An unknown topic, an event that does not encode and a record over the limit cannot be fixed by writing again.
func TestIsLasting(t *testing.T) {
	p, _ := newPublisher(t, ce.Options{MaxMessageBytes: 1024})
	p.AddTopic("t")
	ctx := context.Background()

	for name, err := range map[string]error{
		"unknown topic": p.Push(ctx, "not.added", newEvent(t, map[string]int{"a": 1}), "k"),
		"invalid event": p.Push(ctx, "t", cloudevents.NewEvent(), "k"),
		"too large":     p.Push(ctx, "t", newEvent(t, map[string]string{"pad": strings.Repeat("x", 2048)}), "k"),
	} {
		require.Error(t, err, name)
		assert.True(t, ce.IsLasting(err), name)
		assert.True(t, ce.IsLasting(fmt.Errorf("handler: %w", err)), "%s, wrapped", name)
	}
	assert.False(t, ce.IsLasting(errors.New("other")))
}

// The caller learns it now, not after the relay set the record aside.
func TestPush_RefusesARecordOverTheProducerLimit(t *testing.T) {
	p, w := newPublisher(t, ce.Options{MaxMessageBytes: 1024})
	p.AddTopic("t")

	err := p.Push(context.Background(), "t", newEvent(t, map[string]string{"pad": strings.Repeat("x", 2048)}), "k")
	var configErr sarama.ConfigurationError
	require.ErrorAs(t, err, &configErr)
	assert.Contains(t, err.Error(), "larger than configured Producer.MaxMessageBytes")
	assert.Empty(t, w.topics(), "nothing is written")

	require.NoError(t, p.Push(context.Background(), "t", newEvent(t, map[string]string{"pad": "x"}), "k"))
	assert.Equal(t, []string{"t"}, w.topics())
}

func TestPush_SizeCheckMatchesSarama(t *testing.T) {
	p, _ := newPublisher(t, ce.Options{PartitionKeyExtension: "partitionkey"})
	p.AddTopic("t")

	broker := sarama.NewMockBroker(t, 1)
	defer broker.Close()
	broker.SetHandlerByMap(map[string]sarama.MockResponse{
		"ApiVersionsRequest": sarama.NewMockApiVersionsResponse(t),
		"MetadataRequest": sarama.NewMockMetadataResponse(t).
			SetBroker(broker.Addr(), broker.BrokerID()).
			SetLeader("t", 0, broker.BrokerID()),
		"ProduceRequest": sarama.NewMockProduceResponse(t),
	})
	saramaCfg := sarama.NewConfig()
	saramaCfg.Version = sarama.V3_0_0_0
	saramaCfg.Producer.Return.Successes = true
	saramaCfg.Producer.Retry.Max = 0
	saramaCfg.Net.ReadTimeout = 5 * time.Second
	saramaCfg.Metadata.Retry.Max = 0
	producer, err := sarama.NewSyncProducer([]string{broker.Addr()}, saramaCfg)
	require.NoError(t, err)
	defer func() { _ = producer.Close() }()

	// Push adds the partition key extension itself
	event := func(pad int) cloudevents.Event {
		e := cloudevents.NewEvent()
		e.SetID("8d1a5f1e-0000-4000-8000-000000000001")
		e.SetType("t")
		e.SetSource("test")
		e.SetTime(time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC))
		require.NoError(t, e.SetData(cloudevents.ApplicationJSON, map[string]string{"pad": strings.Repeat("x", pad)}))

		return e
	}
	saramaRefuses := func(pad int) bool {
		e := event(pad)
		e.SetExtension("partitionkey", "company-1")
		msg, encodeErr := ce.Encode(context.Background(), "t", e, "company-1")
		require.NoError(t, encodeErr)
		_, _, sendErr := producer.SendMessage(msg.ProducerMessage())
		var configErr sarama.ConfigurationError
		require.True(t, sendErr == nil || errors.As(sendErr, &configErr), "pad %d: %v", pad, sendErr)

		return sendErr != nil
	}

	limit := saramaCfg.Producer.MaxMessageBytes
	low, high := limit-4096, limit // sarama sends low and refuses high
	require.False(t, saramaRefuses(low))
	require.True(t, saramaRefuses(high))
	for high-low > 1 {
		mid := (low + high) / 2
		if saramaRefuses(mid) {
			high = mid
		} else {
			low = mid
		}
	}

	ctx := context.Background()
	require.NoError(t, p.Push(ctx, "t", event(low), "company-1"), "pad %d: sarama sends it", low)
	var configErr sarama.ConfigurationError
	require.ErrorAs(t, p.Push(ctx, "t", event(high), "company-1"), &configErr, "pad %d: sarama refuses it", high)
}

func TestEncodeEvent_MatchesTheCloudeventsSender(t *testing.T) {
	event := func() cloudevents.Event {
		e := cloudevents.NewEvent()
		e.SetID("8d1a5f1e-0000-4000-8000-000000000001")
		e.SetType("v2.inventory_service.product_stock.updated")
		e.SetSource("inventory")
		e.SetTime(time.Date(2026, 11, 16, 10, 0, 0, 123, time.UTC))
		e.SetExtension("companyid", "company-1")
		e.SetExtension("traceparent", "00-abc-def-01")
		require.NoError(t, e.SetData(cloudevents.ApplicationJSON, map[string]any{"id": "p1", "qty": 2.5}))

		return e
	}

	var want *sarama.ProducerMessage
	producer := mocks.NewSyncProducer(t, nil)
	producer.ExpectSendMessageWithMessageCheckerFunctionAndSucceed(func(msg *sarama.ProducerMessage) error {
		want = msg

		return nil
	})
	sender, err := kafka_sarama.NewSenderFromSyncProducer("topic", producer)
	require.NoError(t, err)
	client, err := cloudevents.NewClient(sender, cloudevents.WithTimeNow(), cloudevents.WithUUIDs())
	require.NoError(t, err)
	result := client.Send(
		kafka_sarama.WithMessageKey(context.Background(), sarama.StringEncoder("company-1")),
		event(),
	)
	require.False(t, cloudevents.IsUndelivered(result), "%v", result)

	got, err := ce.Encode(context.Background(), "topic", event(), "company-1")
	require.NoError(t, err)

	wantValue, err := want.Value.Encode()
	require.NoError(t, err)
	assert.Equal(t, wantValue, got.Value)
	wantKey, err := want.Key.Encode()
	require.NoError(t, err)
	assert.Equal(t, string(wantKey), got.Key)

	// the extensions are written from a map: the order of the headers is not fixed, their set is
	wantHeaders := map[string]string{}
	for _, header := range want.Headers {
		wantHeaders[string(header.Key)] = string(header.Value)
	}
	gotHeaders := map[string]string{}
	for _, header := range got.Headers {
		gotHeaders[header.Key] = string(header.Value)
	}
	require.Len(t, got.Headers, len(want.Headers))
	assert.Equal(t, wantHeaders, gotHeaders)
}

func TestEncodeEvent_DefaultsAndValidation(t *testing.T) {
	e := cloudevents.NewEvent()
	e.SetType("t")
	e.SetSource("s")

	msg, err := ce.Encode(context.Background(), "t", e, "")
	require.NoError(t, err)

	headers := map[string]string{}
	for _, header := range msg.Headers {
		headers[header.Key] = string(header.Value)
	}
	assert.NotEmpty(t, headers["ce_id"])
	assert.NotEmpty(t, headers["ce_time"])

	_, err = ce.Encode(context.Background(), "t", cloudevents.NewEvent(), "")
	require.Error(t, err, "an event without type and source is invalid")
}
