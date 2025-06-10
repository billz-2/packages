package amocrm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEndpoint_Path(t *testing.T) {
	e := endpoint("example")
	path := e.path()
	require.IsType(t, "", path)
	require.Contains(t, path, "/api/v")
	require.Contains(t, path, "/example")
}

func TestLeadsEndpoint(t *testing.T) {
	require.Equal(t, endpoint("leads"), leadsEndpoint)
	path := leadsEndpoint.path()
	require.Contains(t, path, "/api/v")
	require.Contains(t, path, "/leads")
}
