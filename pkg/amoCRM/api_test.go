package amocrm_test

import (
	"context"
	"testing"

	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

func init() {
	// Initialize global logger for tests
	logger.New(logger.LevelDebug, "amocrm-test")
}

func TestNewAPI(t *testing.T) {
	// This test indirectly tests newAPI through the New function in client.go
	cl := amocrm.New(clientID, clientSecret, redirectURL)
	require.NotNil(t, cl)
	require.Implements(t, (*amocrm.Client)(nil), cl)
}

func TestIsValidDomain(t *testing.T) {
	// Test isValidDomain through SetDomain
	cl := amocrm.New(clientID, clientSecret, redirectURL)

	// Valid domains
	require.NoError(t, cl.SetDomain(ctx, "test.amocrm.ru"))
	require.NoError(t, cl.SetDomain(ctx, "test.amocrm.com"))

	// Invalid domains
	require.Error(t, cl.SetDomain(ctx, ""))
	require.Error(t, cl.SetDomain(ctx, "domain"))
	require.Error(t, cl.SetDomain(ctx, "domain.com"))
	require.Error(t, cl.SetDomain(ctx, ".domain.com"))
	require.Error(t, cl.SetDomain(ctx, "www.domain.com"))
	require.Error(t, cl.SetDomain(ctx, "www.amocrm.any"))
}

func TestOAuth2Err(t *testing.T) {
	// Test oauth2Err through getToken
	cl := amocrm.New(clientID, clientSecret, redirectURL)

	// Set a valid domain first
	require.NoError(t, cl.SetDomain(ctx, "test.amocrm.ru"))

	// Test with an invalid grant type
	_, err := cl.TokenByCode(ctx, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth2: fetch token")
}

func TestURL(t *testing.T) {
	// Test url through the Client interface
	cl := amocrm.New(clientID, clientSecret, redirectURL)

	// Set a valid domain first
	require.NoError(t, cl.SetDomain(ctx, "test.amocrm.ru"))

	// We can't directly test url, but we can test that TokenByCode fails with a specific error
	// when the domain is valid but the code is missing
	_, err := cl.TokenByCode(ctx, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth2: fetch token")
}

func TestHeader(t *testing.T) {
	// Test header through the Client interface
	cl := amocrm.New(clientID, clientSecret, redirectURL)

	// Set a valid domain and token
	require.NoError(t, cl.SetDomain(ctx, "test.amocrm.ru"))
	token := amocrm.NewToken(accessToken, refreshToken, tokenType, expiresAt)
	require.NoError(t, cl.SetToken(ctx, token))

	// We can't directly test header, but we can test that the token is set correctly
	// by checking that SetToken doesn't return an error
	require.NoError(t, cl.SetToken(ctx, token))
}

func TestBaseHeader(t *testing.T) {
	// Test baseHeader through the Client interface
	cl := amocrm.New(clientID, clientSecret, redirectURL)

	// We can't directly test baseHeader, but we can test that New creates a valid client
	require.NotNil(t, cl)
	require.Implements(t, (*amocrm.Client)(nil), cl)
}
