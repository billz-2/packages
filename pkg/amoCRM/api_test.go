package amocrm_test

import (
	"testing"

	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/stretchr/testify/require"
)

// BearerPrefix represents the "Bearer" token type prefix used in Authorization headers
// This constant should be used in api.go header() function instead of hardcoded "Bearer" string
// Current implementation in api.go line 141: authHeader := "Bearer" + " " + a.token
// Should be changed to: authHeader := BearerPrefix + " " + a.token
const BearerPrefix = "Bearer"

func init() {
	// Initialize global logger for tests
	logger.New(logger.LevelDebug, "amocrm-test")
}

func TestNewAPI(t *testing.T) {
	// This test indirectly tests newAPI through the New function in client.go
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, testLogger)
	require.NotNil(t, cl)
	require.Implements(t, (*amocrm.Client)(nil), cl)
}

func TestClientCreation(t *testing.T) {
	// Test client creation with valid parameters
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, testLogger)
	require.NotNil(t, cl)

	// Verify client has Leads method
	leads := cl.Leads()
	require.NotNil(t, leads)
}

func TestClientWithEmptyParameters(t *testing.T) {
	// Test client creation with empty parameters
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New("", "", "", "", testLogger)
	require.NotNil(t, cl)
	require.Implements(t, (*amocrm.Client)(nil), cl)
}

func TestClientInterface(t *testing.T) {
	// Test that client implements the Client interface correctly
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, testLogger)

	// Verify interface compliance
	require.Implements(t, (*amocrm.Client)(nil), cl)

	// Verify Leads method returns something
	leads := cl.Leads()
	require.NotNil(t, leads)
}

func TestBearerConstantUsage(t *testing.T) {
	// Test demonstrating how the Bearer constant should be used
	// This test documents the expected Bearer token format
	expectedPrefix := BearerPrefix
	testToken := "test_access_token_123"

	// Expected authorization header format
	expectedAuthHeader := expectedPrefix + " " + testToken

	// Verify the Bearer prefix constant is correct
	require.Equal(t, "Bearer", BearerPrefix)
	require.Equal(t, "Bearer test_access_token_123", expectedAuthHeader)

	// This test serves as documentation for the api.go implementation
	// The header() function should use: authHeader := BearerPrefix + " " + a.token
	// Instead of the current hardcoded: authHeader := "Bearer" + " " + a.token
}
