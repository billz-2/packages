package amocrm_test

import (
	"testing"

	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	"github.com/stretchr/testify/require"
)

func TestBatchUpdateEmptySlice(t *testing.T) {
	// Create a client
	cl := amocrm.New(clientID, clientSecret, redirectURL)
	require.NoError(t, cl.SetDomain(ctx, "test.amocrm.ru"))

	// Call BatchUpdate with empty slice
	err := cl.Leads().BatchUpdate(ctx, []*amocrm.LeadUpdate{})
	require.NoError(t, err)
}

func TestUpdateNilLead(t *testing.T) {
	// Create a client
	cl := amocrm.New(clientID, clientSecret, redirectURL)
	require.NoError(t, cl.SetDomain(ctx, "test.amocrm.ru"))

	// Call Update with nil lead
	err := cl.Leads().Update(ctx, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "lead is nil")
}
