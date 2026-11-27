// Package outboxprom implements outbox.Metrics with Prometheus.
package outboxprom

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

const topicLabel = "topic"

type Metrics struct {
	sent         *prometheus.CounterVec
	sendFailures *prometheus.CounterVec
	setAside     *prometheus.CounterVec
	oldestRowAge prometheus.Gauge
	weakTopics   prometheus.Gauge
}

// New registers the outbox metrics under namespace in reg. A collector already registered there is reused, so
// that a second outbox of the process reports to the same series.
func New(reg prometheus.Registerer, namespace string) (*Metrics, error) {
	m := &Metrics{
		sent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbox_sent_total",
			Help:      "Outbox records acknowledged by Kafka, by topic.",
		}, []string{topicLabel}),
		sendFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbox_send_failures_total",
			Help:      "Failed sends of outbox records (each is tried again), by topic.",
		}, []string{topicLabel}),
		setAside: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbox_set_aside_total",
			Help:      "Outbox records that can never be sent, moved to the failed table, by topic.",
		}, []string{topicLabel}),
		oldestRowAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "outbox_oldest_row_age_seconds",
			Help:      "Age of the oldest outbox row (0 when the table is empty), read by every relay every 15s.",
		}),
		weakTopics: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "outbox_kafka_weak_topics",
			Help:      "Published topics on which acks=all can still lose a record (fewer than 3 replicas, min.insync.replicas below 2 or unclean leader election) or that could not be described; -1 when the check did not run.",
		}),
	}

	var err error
	m.sent, err = register(reg, m.sent)
	if err == nil {
		m.sendFailures, err = register(reg, m.sendFailures)
	}
	if err == nil {
		m.setAside, err = register(reg, m.setAside)
	}
	if err == nil {
		m.oldestRowAge, err = register(reg, m.oldestRowAge)
	}
	if err == nil {
		m.weakTopics, err = register(reg, m.weakTopics)
	}
	if err != nil {
		return nil, err
	}

	return m, nil
}

func register[C prometheus.Collector](reg prometheus.Registerer, c C) (C, error) {
	err := reg.Register(c)

	var registered prometheus.AlreadyRegisteredError
	if errors.As(err, &registered) {
		existing, ok := registered.ExistingCollector.(C)
		if !ok {
			return c, err
		}

		return existing, nil
	}

	return c, err
}

func (m *Metrics) Sent(topic string) { m.sent.WithLabelValues(topic).Inc() }

func (m *Metrics) SendFailed(topic string) { m.sendFailures.WithLabelValues(topic).Inc() }

func (m *Metrics) SetAside(topic string) { m.setAside.WithLabelValues(topic).Inc() }

func (m *Metrics) OldestRowAge(seconds float64) { m.oldestRowAge.Set(seconds) }

func (m *Metrics) WeakTopics(n int) { m.weakTopics.Set(float64(n)) }
