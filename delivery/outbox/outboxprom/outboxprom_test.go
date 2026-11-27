package outboxprom //nolint:testpackage // reads the collectors

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The alerts of the runbook use these names.
func TestOutboxprom_KeepsTheMetricNames(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := New(reg, "billz_inventory_service_v2")
	require.NoError(t, err)

	m.Sent("t")
	m.SendFailed("t")
	m.SetAside("t")
	m.OldestRowAge(42)
	m.WeakTopics(3)

	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP billz_inventory_service_v2_outbox_kafka_weak_topics Published topics on which acks=all can still lose a record (fewer than 3 replicas, min.insync.replicas below 2 or unclean leader election) or that could not be described; -1 when the check did not run.
# TYPE billz_inventory_service_v2_outbox_kafka_weak_topics gauge
billz_inventory_service_v2_outbox_kafka_weak_topics 3
# HELP billz_inventory_service_v2_outbox_oldest_row_age_seconds Age of the oldest outbox row (0 when the table is empty), read by every relay every 15s.
# TYPE billz_inventory_service_v2_outbox_oldest_row_age_seconds gauge
billz_inventory_service_v2_outbox_oldest_row_age_seconds 42
# HELP billz_inventory_service_v2_outbox_send_failures_total Failed sends of outbox records (each is tried again), by topic.
# TYPE billz_inventory_service_v2_outbox_send_failures_total counter
billz_inventory_service_v2_outbox_send_failures_total{topic="t"} 1
# HELP billz_inventory_service_v2_outbox_sent_total Outbox records acknowledged by Kafka, by topic.
# TYPE billz_inventory_service_v2_outbox_sent_total counter
billz_inventory_service_v2_outbox_sent_total{topic="t"} 1
# HELP billz_inventory_service_v2_outbox_set_aside_total Outbox records that can never be sent, moved to the failed table, by topic.
# TYPE billz_inventory_service_v2_outbox_set_aside_total counter
billz_inventory_service_v2_outbox_set_aside_total{topic="t"} 1
`)))
}

func TestNew_ReusesTheRegisteredCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	first, err := New(reg, "ns")
	require.NoError(t, err)
	second, err := New(reg, "ns")
	require.NoError(t, err)

	first.Sent("t")
	second.Sent("t")
	require.InDelta(t, 2.0, testutil.ToFloat64(first.sent.WithLabelValues("t")), 0)
}
