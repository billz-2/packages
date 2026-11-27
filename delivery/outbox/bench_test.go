package outbox //nolint:testpackage // measures the relay against a real PostgreSQL

import (
	"fmt"
	"hash/fnv"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
)

// BenchmarkRelay measures one relay with the PostgreSQL of OUTBOX_TEST_POSTGRES_DSN and a fake Kafka that
// charges each call the round trips the producer config takes for it (benchConfigs). burst_mix is the
// 133 msg/s burst of one key in the 240 msg/s peak; catch_up is 10 minutes of that peak left by a Kafka outage.
// OUTBOX_BENCH_CONFIGS picks the configs (default B,C). With OUTBOX_BENCH_BROKERS (host:port,...) it sends to
// those brokers instead, through ProducerConfig, to topics created beforehand.
//
// Run: go test -run '^$' -bench BenchmarkRelay -benchtime 1x ./outbox/
func BenchmarkRelay(b *testing.B) {
	for _, cfg := range benchConfigs(b) {
		for _, latency := range cfg.latencies {
			for _, tc := range []struct {
				name    string
				records int
				key     func(i int) (topic, key string)
			}{
				{name: "one_key", records: 1000, key: func(int) (string, string) { return benchTopic, burstKey }},
				{name: "200_keys", records: 20000, key: func(i int) (string, string) {
					return benchTopic, "company-" + strconv.Itoa(i%200)
				}},
				{name: "burst_mix", records: 2000, key: func(i int) (string, string) {
					if i%20 < 11 {
						return benchTopic, burstKey
					}

					return benchTopic, "company-" + strconv.Itoa(i%50)
				}},
				{name: "other_topics", records: 2000, key: func(i int) (string, string) {
					if i%4 < 3 {
						return benchTopic, burstKey
					}

					return "v2.inventory_service.other_" + strconv.Itoa(i%10), "company-" + strconv.Itoa(i%5)
				}},
				{name: "50_topics", records: 20000, key: spread},
				{name: "catch_up_50_topics", records: 144000, key: spread},
			} {
				b.Run(fmt.Sprintf("%s/config=%s/latency=%s", tc.name, cfg.name, latency), func(b *testing.B) {
					for range b.N {
						benchmarkRelay(b, cfg.producer(b, latency), tc.records, tc.key)
					}
				})
			}
		}
	}
}

type benchConfig struct {
	name      string
	latencies []time.Duration
	producer  func(b *testing.B, latency time.Duration) Producer
}

// benchBrokers is the size of the fake cluster: a record's broker is the leader of its partition.
const benchBrokers = 3

// benchConfigs are the producer configs of the acks decision:
//   - A, staging's: acks=1 and 5 requests in flight, so a wave takes one round trip of the leader (L);
//   - B, ProducerConfig: one request in flight, so a broker takes a wave's first record alone, the few
//     behind it and then the rest, one round trip of the in-sync replicas (L+Δ) each (TestWaveRequests_PerBroker);
//   - C, B with Flush.Frequency 1ms: one request per broker, 1 ms later.
func benchConfigs(b *testing.B) []benchConfig {
	b.Helper()

	brokers := os.Getenv("OUTBOX_BENCH_BROKERS")
	type waveTime func(latency time.Duration, msgs []*sarama.ProducerMessage) time.Duration
	fake := func(name string, latencies []time.Duration, wave waveTime) benchConfig {
		producer := func(_ *testing.B, latency time.Duration) Producer {
			return &fakeKafka{delay: func(msgs []*sarama.ProducerMessage) time.Duration { return wave(latency, msgs) }}
		}

		return benchConfig{name: name, latencies: latencies, producer: producer}
	}
	onBrokers := func(name string, flush time.Duration) benchConfig {
		producer := func(b *testing.B, _ time.Duration) Producer {
			b.Helper()

			return realProducer(b, brokers, flush)
		}

		return benchConfig{name: name, latencies: []time.Duration{0}, producer: producer}
	}

	all := map[string]benchConfig{
		"A": fake("A", []time.Duration{2 * time.Millisecond, 5 * time.Millisecond, 8 * time.Millisecond},
			func(latency time.Duration, _ []*sarama.ProducerMessage) time.Duration { return latency }),
		"B": fake("B", []time.Duration{3 * time.Millisecond, 7 * time.Millisecond, 11 * time.Millisecond},
			func(latency time.Duration, msgs []*sarama.ProducerMessage) time.Duration {
				return latency * time.Duration(min(mostPerBroker(msgs), 3))
			}),
		"C": fake("C", []time.Duration{3 * time.Millisecond, 7 * time.Millisecond, 11 * time.Millisecond},
			func(latency time.Duration, _ []*sarama.ProducerMessage) time.Duration {
				return latency + time.Millisecond
			}),
	}
	if brokers != "" {
		all = map[string]benchConfig{"B": onBrokers("B", 0), "C": onBrokers("C", time.Millisecond)}
	}

	names := os.Getenv("OUTBOX_BENCH_CONFIGS")
	if names == "" {
		names = "B,C"
	}
	var configs []benchConfig
	for _, name := range strings.Split(names, ",") {
		cfg, ok := all[name]
		if !ok {
			b.Fatalf("OUTBOX_BENCH_CONFIGS: no config %q", name)
		}
		configs = append(configs, cfg)
	}

	return configs
}

// counting is a fake that counts what producer sent; the relay sets each message's index as its Metadata.
func counting(producer Producer) *fakeKafka {
	var failed sync.Map
	kafka := &fakeKafka{delay: func([]*sarama.ProducerMessage) time.Duration { return 0 }}
	kafka.before = func(sent []*sarama.ProducerMessage) {
		for i, err := range sendErrors(producer.SendMessages(sent), len(sent)) {
			if err != nil {
				failed.Store(sent[i], err)
			}
		}
	}
	kafka.fail = func(msg *sarama.ProducerMessage) (error, bool) {
		if err, ok := failed.LoadAndDelete(msg); ok {
			return err.(error), false //nolint:forcetypeassert // only errors are stored
		}

		return nil, true
	}

	return kafka
}

// mostPerBroker is the most records of a call that one broker of the fake cluster leads.
func mostPerBroker(msgs []*sarama.ProducerMessage) int {
	var counts [benchBrokers]int
	for _, msg := range msgs {
		h := fnv.New32a()
		_, _ = h.Write([]byte(msg.Topic + "/" + key(msg)))
		counts[h.Sum32()%benchBrokers]++
	}

	return slices.Max(counts[:])
}

func realProducer(b *testing.B, brokers string, flush time.Duration) Producer {
	b.Helper()

	c := sarama.NewConfig()
	c.Version = sarama.V3_0_0_0
	ProducerConfig(c)
	c.Producer.Flush.Frequency = flush
	client, err := sarama.NewClient(strings.Split(brokers, ","), c)
	if err != nil {
		b.Fatal(err)
	}
	producer, err := NewProducer(client)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_ = producer.Close()
		_ = client.Close()
	})

	return producer
}

const (
	benchTopic = "v2.inventory_service.product_movement.updated"
	burstKey   = "company-burst"
)

func spread(i int) (topic, key string) {
	return "v2.inventory_service.spread_" + strconv.Itoa(i%50), "company-" + strconv.Itoa(i%200)
}

func benchmarkRelay(b *testing.B, producer Producer, records int, keyOf func(i int) (string, string)) {
	b.StopTimer()
	env := testDB(b)

	msgs := make([]Message, 0, records)
	for i := range records {
		topic, key := keyOf(i)
		msgs = append(msgs, msgOf(topic, key, i))
	}
	env.publish(b, msgs...)
	// statistics, as autovacuum keeps them on a table with this much churn
	if _, err := env.conn.Exec(`ANALYZE outbox`); err != nil {
		b.Fatal(err)
	}

	var (
		mu                sync.Mutex
		burst, others     int
		burstAt, othersAt time.Time
		start             time.Time
	)
	kafka := counting(producer)
	kafka.after = func(sent []*sarama.ProducerMessage) {
		mu.Lock()
		defer mu.Unlock()
		for _, msg := range sent {
			if msg.Topic == benchTopic && key(msg) == burstKey {
				burst++
				burstAt = time.Now()
			} else {
				others++
				othersAt = time.Now()
			}
		}
	}

	// the defaults of production, not the short pauses of the tests
	r, err := env.ob.Relay(env.pool(b, "bench").DB, kafka, RelayOptions{LockKey: env.lock})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = r.Close() })

	b.StartTimer()
	start = time.Now()
	r.Start()
	// the fake's count first: a count(*) of a large outbox every millisecond would load the database
	for kafka.count() < records {
		time.Sleep(time.Millisecond)
	}
	for env.rows(b, "outbox") > 0 {
		time.Sleep(time.Millisecond)
	}
	elapsed := time.Since(start)
	b.StopTimer()

	if kafka.count() != records {
		b.Fatalf("sent %d records, want %d", kafka.count(), records)
	}
	b.ReportMetric(float64(records)/elapsed.Seconds(), "msg/s")
	b.ReportMetric(float64(kafka.sentCalls())/elapsed.Seconds(), "calls/s")

	mu.Lock()
	defer mu.Unlock()
	if burst > 0 && others > 0 {
		b.ReportMetric(float64(burst)/burstAt.Sub(start).Seconds(), "burst_msg/s")
		b.ReportMetric(float64(others)/othersAt.Sub(start).Seconds(), "others_msg/s")
	}
}
