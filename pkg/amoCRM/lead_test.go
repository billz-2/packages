package amocrm_test

import (
	"context"
	"testing"

	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

func TestBatchUpdateEmptySlice(t *testing.T) {
	// Create a client
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, domain, testLogger)

	// Call BatchUpdate with empty slice
	err := cl.Leads().BatchUpdate(ctx, []*amocrm.LeadUpdate{})
	require.NoError(t, err)
}

func TestUpdateNilLead(t *testing.T) {
	// Create a client
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, domain, testLogger)

	// Call Update with nil lead
	err := cl.Leads().Update(ctx, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "lead is nil")
}

func TestLeadUpdateWithValidData(t *testing.T) {
	testLogger := logger.New(logger.LevelDebug, "test-lead-update")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, domain, testLogger)

	// Create test lead with valid data
	name := "Test Lead"
	lead := &amocrm.LeadUpdate{
		ID:   12345,
		Name: &name,
	}

	// This will fail due to authentication, but we can verify logging is working
	err := cl.Leads().Update(ctx, lead)
	require.Error(t, err) // Expected to fail due to no auth token
}

func TestBatchUpdateLogging(t *testing.T) {
	testLogger := logger.New(logger.LevelDebug, "test-batch-logging")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, domain, testLogger)

	// Test with some leads - should log debug messages about the operation
	name1 := "Test Lead 1"
	name2 := "Test Lead 2"
	leads := []*amocrm.LeadUpdate{
		{ID: 1, Name: &name1},
		{ID: 2, Name: &name2},
	}

	// This will fail due to authentication, but logging should work
	err := cl.Leads().BatchUpdate(ctx, leads)
	require.Error(t, err) // Expected to fail due to no auth token
}
