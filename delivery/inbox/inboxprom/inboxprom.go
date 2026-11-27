// Package inboxprom implements inbox.Metrics with Prometheus.
package inboxprom

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

const name = "consumer"

type Metrics struct {
	messages  *prometheus.CounterVec
	deadRows  prometheus.Gauge
	queuedAge *prometheus.GaugeVec
	txSeconds *prometheus.HistogramVec
	txWait    *prometheus.HistogramVec
}

// New registers the inbox metrics under namespace in reg. A collector already registered there is reused.
func New(reg prometheus.Registerer, namespace string) (*Metrics, error) {
	m := &Metrics{
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "inbox_messages_total",
			Help:      "Consumed messages by consumer and result: done, duplicate, retry (an attempt failed), dead or queued.",
		}, []string{name, "result"}),
		deadRows: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "inbox_dead_rows",
			Help:      "Dead letters in the inbox table, read every 60s.",
		}),
		queuedAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "inbox_queued_oldest_age_seconds",
			Help:      "Age of the oldest queued message of a deferred consumer (0 when none), read every 60s.",
		}, []string{name}),
		txSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "inbox_tx_duration_seconds",
			Help:      "Duration of the transaction of one attempt of a same-transaction consumer.",
			Buckets:   []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{name}),
		txWait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "inbox_tx_slot_wait_seconds",
			Help:      "Wait of a same-transaction message for one of the transaction slots its consumers share.",
			Buckets:   []float64{0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{name}),
	}

	var err error
	m.messages, err = register(reg, m.messages)
	if err == nil {
		m.deadRows, err = register(reg, m.deadRows)
	}
	if err == nil {
		m.queuedAge, err = register(reg, m.queuedAge)
	}
	if err == nil {
		m.txSeconds, err = register(reg, m.txSeconds)
	}
	if err == nil {
		m.txWait, err = register(reg, m.txWait)
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

func (m *Metrics) Message(consumer, result string) {
	m.messages.WithLabelValues(consumer, result).Inc()
}

func (m *Metrics) DeadRows(n int) { m.deadRows.Set(float64(n)) }

func (m *Metrics) QueuedOldestAge(consumer string, seconds float64) {
	m.queuedAge.WithLabelValues(consumer).Set(seconds)
}

func (m *Metrics) TxDuration(consumer string, seconds float64) {
	m.txSeconds.WithLabelValues(consumer).Observe(seconds)
}

func (m *Metrics) TxSlotWait(consumer string, seconds float64) {
	m.txWait.WithLabelValues(consumer).Observe(seconds)
}
