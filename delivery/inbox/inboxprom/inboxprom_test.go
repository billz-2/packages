package inboxprom //nolint:testpackage // reads the collectors

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The alerts of the runbook use these names.
func TestInboxprom_KeepsTheMetricNames(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := New(reg, "billz_inventory_service_v2")
	require.NoError(t, err)

	m.Message("t", "dead")
	m.DeadRows(2)
	m.QueuedOldestAge("t", 30)

	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP billz_inventory_service_v2_inbox_dead_rows Dead letters in the inbox table, read every 60s.
# TYPE billz_inventory_service_v2_inbox_dead_rows gauge
billz_inventory_service_v2_inbox_dead_rows 2
# HELP billz_inventory_service_v2_inbox_messages_total Consumed messages by consumer and result: done, duplicate, retry (an attempt failed), dead or queued.
# TYPE billz_inventory_service_v2_inbox_messages_total counter
billz_inventory_service_v2_inbox_messages_total{consumer="t",result="dead"} 1
# HELP billz_inventory_service_v2_inbox_queued_oldest_age_seconds Age of the oldest queued message of a deferred consumer (0 when none), read every 60s.
# TYPE billz_inventory_service_v2_inbox_queued_oldest_age_seconds gauge
billz_inventory_service_v2_inbox_queued_oldest_age_seconds{consumer="t"} 30
`)))

	m.TxDuration("t", 0.3)
	require.Equal(t, 1, testutil.CollectAndCount(m.txSeconds, "billz_inventory_service_v2_inbox_tx_duration_seconds"))
	m.TxSlotWait("t", 0.1)
	require.Equal(t, 1, testutil.CollectAndCount(m.txWait, "billz_inventory_service_v2_inbox_tx_slot_wait_seconds"))
}

func TestNew_ReusesTheRegisteredCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	first, err := New(reg, "ns")
	require.NoError(t, err)
	second, err := New(reg, "ns")
	require.NoError(t, err)

	first.Message("t", "done")
	second.Message("t", "done")
	require.InDelta(t, 2.0, testutil.ToFloat64(first.messages.WithLabelValues("t", "done")), 0)
}
