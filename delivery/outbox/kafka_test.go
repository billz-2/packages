package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/billz-2/packages/delivery/outbox"
)

func producerConfig() *sarama.Config {
	c := sarama.NewConfig()
	c.Version = sarama.V3_0_0_0
	outbox.ProducerConfig(c)

	return c
}

func TestProducerConfig_PassesSaramaValidation(t *testing.T) {
	c := producerConfig()
	require.NoError(t, c.Validate())

	assert.Equal(t, sarama.WaitForAll, c.Producer.RequiredAcks)
	assert.True(t, c.Producer.Idempotent)
	assert.Equal(t, 1, c.Net.MaxOpenRequests)
	assert.Equal(t, 3, c.Producer.Retry.Max)
	assert.True(t, c.Producer.Return.Successes)

	c = sarama.NewConfig()
	c.Producer.Retry.Max = 0
	outbox.ProducerConfig(c)
	assert.Equal(t, 3, c.Producer.Retry.Max, "an idempotent producer needs retries")
	c.Producer.Retry.Max = 10
	outbox.ProducerConfig(c)
	assert.Equal(t, 10, c.Producer.Retry.Max, "a higher value is kept")
}

func cluster(t testing.TB, n int, topics []string, partitions int32) []*sarama.MockBroker {
	t.Helper()

	brokers := make([]*sarama.MockBroker, n)
	for i := range brokers {
		brokers[i] = sarama.NewMockBroker(t, int32(i+1))
		t.Cleanup(brokers[i].Close)
	}
	metadata := sarama.NewMockMetadataResponse(t).SetController(brokers[0].BrokerID())
	for _, b := range brokers {
		metadata.SetBroker(b.Addr(), b.BrokerID())
	}
	for i, topic := range topics {
		for p := range partitions {
			metadata.SetLeader(topic, p, brokers[(i+int(p))%n].BrokerID())
		}
	}
	for _, b := range brokers {
		b.SetHandlerByMap(map[string]sarama.MockResponse{
			"ApiVersionsRequest":    sarama.NewMockApiVersionsResponse(t),
			"MetadataRequest":       metadata,
			"InitProducerIDRequest": sarama.NewMockInitProducerIDResponse(t),
			"ProduceRequest":        sarama.NewMockProduceResponse(t),
		})
	}

	return brokers
}

func TestNewProducer_RefusesAWeakClient(t *testing.T) {
	brokers := cluster(t, 1, []string{"t"}, 1)

	weak := sarama.NewConfig()
	weak.Version = sarama.V3_0_0_0
	weak.Producer.Return.Successes = true
	client, err := sarama.NewClient([]string{brokers[0].Addr()}, weak)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	_, err = outbox.NewProducer(client)
	require.Error(t, err)
	for _, setting := range []string{"WaitForAll", "Idempotent", "MaxOpenRequests", "outbox.ProducerConfig"} {
		assert.Contains(t, err.Error(), setting)
	}

	strong, err := sarama.NewClient([]string{brokers[0].Addr()}, producerConfig())
	require.NoError(t, err)
	defer func() { _ = strong.Close() }()
	producer, err := outbox.NewProducer(strong)
	require.NoError(t, err)
	defer func() { _ = producer.Close() }()
	_, _, err = producer.SendMessage(&sarama.ProducerMessage{Topic: "t", Value: sarama.StringEncoder("v")})
	require.NoError(t, err)
}

func TestReportDurability_MinusOneWhenNothingWasChecked(t *testing.T) {
	for name, check := range map[string]func(ob *outbox.Outbox){
		"no admin":  func(ob *outbox.Outbox) { ob.ReportDurability(context.Background(), nil, []string{"t"}, 1) },
		"no topics": func(ob *outbox.Outbox) { ob.ReportDurability(context.Background(), &fakeAdmin{}, nil, 1) },
		"every topic failed": func(ob *outbox.Outbox) {
			admin := &fakeAdmin{replicas: map[string]int{"t": 3},
				configErr: map[string]error{"t": sarama.ErrTopicAuthorizationFailed}}
			ob.ReportDurability(context.Background(), admin, []string{"t"}, 1)
		},
	} {
		metrics := &weakMetrics{weak: 7}
		ob, err := outbox.New(noDB{}, outbox.Options{Service: "s", Logger: &recordedLog{}, Metrics: metrics})
		require.NoError(t, err)
		check(ob)
		assert.Equal(t, -1, metrics.weak, name)
	}
}

// sarama itself refuses an idempotent config without acks=all and one open request, so a weak client is
// one that is not idempotent.
func TestNewProducer_NamesEachMissingSetting(t *testing.T) {
	brokers := cluster(t, 1, []string{"t"}, 1)
	for setting, weaken := range map[string]func(*sarama.Config){
		"Idempotent":   func(c *sarama.Config) { c.Producer.Idempotent = false },
		"RequiredAcks": func(c *sarama.Config) { c.Producer.Idempotent, c.Producer.RequiredAcks = false, sarama.WaitForLocal },
		"MaxOpenRequests": func(c *sarama.Config) {
			c.Producer.Idempotent, c.Net.MaxOpenRequests = false, 5
		},
	} {
		c := producerConfig()
		weaken(c)
		client, err := sarama.NewClient([]string{brokers[0].Addr()}, c)
		require.NoError(t, err)
		_, err = outbox.NewProducer(client)
		require.Error(t, err, setting)
		assert.Contains(t, err.Error(), setting)
		_ = client.Close()
	}
}

// waveRequests sends 20 waves of records through 3 MockBrokers and counts the waves by the produce requests of
// their busiest broker.
func waveRequests(tb testing.TB, flush time.Duration, records int) map[int]int {
	tb.Helper()

	topics := make([]string, 50)
	for i := range topics {
		topics[i] = "topic_" + strconv.Itoa(i)
	}
	brokers := cluster(tb, 3, topics, 3)
	addrs := make([]string, len(brokers))
	for i, b := range brokers {
		b.SetLatency(5 * time.Millisecond)
		addrs[i] = b.Addr()
	}

	c := producerConfig()
	c.Producer.Flush.Frequency = flush
	producer, err := sarama.NewSyncProducer(addrs, c)
	require.NoError(tb, err)
	defer func() { _ = producer.Close() }()

	wave := func(n int) []*sarama.ProducerMessage {
		msgs := make([]*sarama.ProducerMessage, 0, n)
		for i := range n {
			msgs = append(msgs, &sarama.ProducerMessage{
				Topic: topics[i%len(topics)], Key: sarama.StringEncoder("key-" + strconv.Itoa(i)),
				Value: sarama.StringEncoder("v"),
			})
		}

		return msgs
	}
	require.NoError(tb, producer.SendMessages(wave(len(topics))), "warm-up: metadata of every topic")

	waves := map[int]int{}
	for range 20 {
		before := produceRequests(brokers)
		require.NoError(tb, producer.SendMessages(wave(records)))
		most := 0
		for i, after := range produceRequests(brokers) {
			most = max(most, after-before[i])
		}
		waves[most]++
	}

	return waves
}

func produceRequests(brokers []*sarama.MockBroker) []int {
	counts := make([]int, len(brokers))
	for i, b := range brokers {
		for _, rr := range b.History() {
			if _, ok := rr.Request.(*sarama.ProduceRequest); ok {
				counts[i]++
			}
		}
	}

	return counts
}

// One record per broker goes in one request per broker, whatever the scheduler does (the 3 keys hash to
// partitions on 3 brokers). How a wave of many records splits is timing; BenchmarkWaveRequests measures it.
func TestWaveRequests_PerBroker(t *testing.T) {
	assert.Equal(t, map[int]int{1: 20}, waveRequests(t, 0, 3))
}

// BenchmarkWaveRequests reports the produce requests of a wave of 100 records on its busiest broker. With
// MaxOpenRequests 1 and the default Flush a broker typically gets the first record alone, the few behind it in a
// second request that waits for the first answer, and the rest in a third; Flush.Frequency bundles the wave.
// BenchmarkRelay charges a wave these 3 round trips.
//
// Run: go test -run '^$' -bench BenchmarkWaveRequests -benchtime 5x ./outbox/
func BenchmarkWaveRequests(b *testing.B) {
	for _, flush := range []time.Duration{0, time.Millisecond, 10 * time.Millisecond} {
		b.Run("flush "+flush.String(), func(b *testing.B) {
			all := map[int]int{}
			for range b.N {
				for n, waves := range waveRequests(b, flush, 100) {
					all[n] += waves
				}
			}
			mode, total, sum := 0, 0, 0
			for n, waves := range all {
				if waves > all[mode] {
					mode = n
				}
				total += waves
				sum += n * waves
			}
			b.Logf("waves by the requests of their busiest broker: %v", all)
			b.ReportMetric(float64(mode), "mode-requests/wave")
			b.ReportMetric(float64(sum)/float64(total), "requests/wave")
		})
	}
}

type fakeAdmin struct {
	replicas  map[string]int
	configs   map[string]map[string]string
	configErr map[string]error
	block     chan struct{}
}

func (a *fakeAdmin) DescribeTopics(topics []string) ([]*sarama.TopicMetadata, error) {
	if a.block != nil {
		<-a.block
	}
	n, ok := a.replicas[topics[0]]
	if !ok {
		return []*sarama.TopicMetadata{{Name: topics[0], Err: sarama.ErrUnknownTopicOrPartition}}, nil
	}

	return []*sarama.TopicMetadata{{Name: topics[0], Partitions: []*sarama.PartitionMetadata{
		{ID: 0, Replicas: make([]int32, n)}, {ID: 1, Replicas: make([]int32, n)},
	}}}, nil
}

func (a *fakeAdmin) DescribeConfig(resource sarama.ConfigResource) ([]sarama.ConfigEntry, error) {
	if err := a.configErr[resource.Name]; err != nil {
		return nil, err
	}
	var entries []sarama.ConfigEntry
	for name, value := range a.configs[resource.Name] {
		entries = append(entries, sarama.ConfigEntry{Name: name, Value: value})
	}

	return entries, nil
}

func TestCheckDurability(t *testing.T) {
	strong := map[string]string{"min.insync.replicas": "2", "unclean.leader.election.enable": "false"}
	admin := &fakeAdmin{
		replicas: map[string]int{"strong": 3, "one_replica": 1, "min_isr_1": 3, "unclean": 3, "denied": 3},
		configs: map[string]map[string]string{
			"strong":      strong,
			"one_replica": strong,
			"min_isr_1":   {"min.insync.replicas": "1"},
			"unclean":     {"min.insync.replicas": "2", "unclean.leader.election.enable": "true"},
		},
		configErr: map[string]error{"denied": sarama.ErrTopicAuthorizationFailed},
	}
	topics := []string{"strong", "one_replica", "min_isr_1", "unclean", "denied", "missing"}

	got := outbox.CheckDurability(context.Background(), admin, topics, 2)
	require.Len(t, got, len(topics))
	weak := map[string]bool{}
	for i, d := range got {
		assert.Equal(t, topics[i], d.Topic)
		weak[d.Topic] = d.Weak()
	}
	assert.Equal(t, map[string]bool{
		"strong": false, "one_replica": true, "min_isr_1": true, "unclean": true, "denied": false, "missing": false,
	}, weak)
	assert.Equal(t, outbox.TopicDurability{Topic: "strong", Replicas: 3, MinInsync: 2}, got[0])
	require.ErrorIs(t, got[4].Err, sarama.ErrTopicAuthorizationFailed, "a topic it may not describe is reported")
	require.ErrorIs(t, got[5].Err, sarama.ErrUnknownTopicOrPartition)
}

func TestCheckDurability_StopsAtItsDeadline(t *testing.T) {
	admin := &fakeAdmin{replicas: map[string]int{"t": 3}, block: make(chan struct{})}
	defer close(admin.block)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got := outbox.CheckDurability(ctx, admin, []string{"t", "u"}, 1)
	require.Len(t, got, 2)
	for _, d := range got {
		require.ErrorIs(t, d.Err, context.DeadlineExceeded)
		assert.False(t, d.Weak(), "not checked is not weak")
	}
}

func TestCheckDurability_ThroughAClusterAdmin(t *testing.T) {
	brokers := cluster(t, 3, []string{"strong", "weak", "denied"}, 1)
	configs := sarama.NewMockWrapper(&sarama.DescribeConfigsResponse{Version: 2, Resources: []*sarama.ResourceResponse{
		{Name: "strong", Type: sarama.TopicResource, Configs: []*sarama.ConfigEntry{
			{Name: "min.insync.replicas", Value: "2"}, {Name: "unclean.leader.election.enable", Value: "false"},
		}},
		{Name: "weak", Type: sarama.TopicResource, Configs: []*sarama.ConfigEntry{
			{Name: "min.insync.replicas", Value: "1"},
		}},
		{Name: "denied", Type: sarama.TopicResource, ErrorCode: int16(sarama.ErrTopicAuthorizationFailed)},
	}})
	for _, b := range brokers {
		b.SetHandlerByMap(map[string]sarama.MockResponse{
			"ApiVersionsRequest":     sarama.NewMockApiVersionsResponse(t),
			"MetadataRequest":        brokersMetadata(t, brokers, "strong", "weak", "denied"),
			"DescribeConfigsRequest": configs,
		})
	}

	c := sarama.NewConfig()
	c.Version = sarama.V3_0_0_0
	admin, err := sarama.NewClusterAdmin([]string{brokers[0].Addr()}, c)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()

	got := outbox.CheckDurability(context.Background(), admin, []string{"strong", "weak", "denied"}, 3)
	require.NoError(t, got[0].Err)
	assert.Equal(t, outbox.TopicDurability{Topic: "strong", Replicas: 3, MinInsync: 2}, got[0])
	require.NoError(t, got[1].Err)
	assert.True(t, got[1].Weak())
	var configErr *sarama.DescribeConfigError
	require.ErrorAs(t, got[2].Err, &configErr)
}

func brokersMetadata(t *testing.T, brokers []*sarama.MockBroker, topics ...string) *sarama.MockMetadataResponse {
	t.Helper()

	metadata := sarama.NewMockMetadataResponse(t).SetController(brokers[0].BrokerID())
	for _, b := range brokers {
		metadata.SetBroker(b.Addr(), b.BrokerID())
	}
	for _, topic := range topics {
		metadata.SetLeader(topic, 0, brokers[0].BrokerID())
	}

	return metadata
}

type recordedLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordedLog) add(level, msg string, args []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+msg+" "+fmt.Sprint(args...))
}

func (l *recordedLog) Info(msg string, args ...any)  { l.add("INFO", msg, args) }
func (l *recordedLog) Warn(msg string, args ...any)  { l.add("WARN", msg, args) }
func (l *recordedLog) Error(msg string, args ...any) { l.add("ERROR", msg, args) }

type weakMetrics struct{ weak int }

func (*weakMetrics) Sent(string)          {}
func (*weakMetrics) SendFailed(string)    {}
func (*weakMetrics) SetAside(string)      {}
func (*weakMetrics) OldestRowAge(float64) {}
func (m *weakMetrics) WeakTopics(n int)   { m.weak = n }

type noDB struct{}

func (noDB) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("not used")
}

func (noDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("not used")
}

func TestReportDurability_LogsAndCountsTheWeakTopics(t *testing.T) {
	log, metrics := &recordedLog{}, &weakMetrics{}
	ob, err := outbox.New(noDB{}, outbox.Options{Service: "s", Logger: log, Metrics: metrics})
	require.NoError(t, err)

	strong := map[string]string{"min.insync.replicas": "2"}
	admin := &fakeAdmin{
		replicas:  map[string]int{"strong": 3, "rf1": 1, "isr1": 3, "denied": 3},
		configs:   map[string]map[string]string{"strong": strong, "rf1": strong, "isr1": {"min.insync.replicas": "1"}},
		configErr: map[string]error{"denied": sarama.ErrTopicAuthorizationFailed},
	}
	ob.ReportDurability(context.Background(), admin, []string{"strong", "rf1", "isr1", "denied"}, 2)

	assert.Equal(t, 3, metrics.weak, "rf1 and isr1 are weak, denied could not be checked")
	var errorsFor, warnsFor []string
	for _, line := range log.lines {
		fields := strings.Fields(line)
		switch fields[0] {
		case "ERROR":
			assert.Contains(t, line, "a broker loss can stop or lose records the outbox already deleted")
			errorsFor = append(errorsFor, line)
		case "WARN":
			warnsFor = append(warnsFor, line)
		}
	}
	require.Len(t, errorsFor, 2)
	assert.Contains(t, errorsFor[0]+errorsFor[1], "rf1")
	assert.Contains(t, errorsFor[0]+errorsFor[1], "isr1")
	require.Len(t, warnsFor, 1)
	assert.Contains(t, warnsFor[0], "denied")
}

// A topic it could not describe counts even when the others were described: it may be weak.
func TestReportDurability_CountsATopicItCouldNotCheck(t *testing.T) {
	strong := map[string]string{"min.insync.replicas": "2"}
	for name, c := range map[string]struct {
		topics []string
		want   int
	}{
		"all described": {topics: []string{"a", "b"}, want: 0},
		"one denied":    {topics: []string{"a", "denied"}, want: 1},
		"all denied":    {topics: []string{"denied"}, want: -1},
	} {
		metrics := &weakMetrics{}
		ob, err := outbox.New(noDB{}, outbox.Options{Service: "s", Logger: &recordedLog{}, Metrics: metrics})
		require.NoError(t, err)
		admin := &fakeAdmin{
			replicas:  map[string]int{"a": 3, "b": 3, "denied": 3},
			configs:   map[string]map[string]string{"a": strong, "b": strong},
			configErr: map[string]error{"denied": sarama.ErrTopicAuthorizationFailed},
		}
		ob.ReportDurability(context.Background(), admin, c.topics, 2)
		assert.Equal(t, c.want, metrics.weak, name)
	}
}
