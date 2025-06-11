package amocrm_test

import (
	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	"strings"
	"testing"
	"time"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/stretchr/testify/require"
)

var (
	clientID     = "client_id"
	clientSecret = "client_secret"
	redirectURL  = "redirect_url"
)

func TestNew(t *testing.T) {
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, redirectURL, testLogger)
	require.Implements(t, (*amocrm.Client)(nil), cl)
}

func TestAmoCRM_SetToken(t *testing.T) {
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, redirectURL, testLogger)
	require.EqualError(t, cl.SetToken(ctx, nil), "invalid token")

	token := amocrm.NewToken(accessToken, refreshToken, tokenType, time.Now())
	require.NoError(t, cl.SetToken(ctx, token))
}

func TestAmoCRM_SetDomain(t *testing.T) {
	cases := []struct {
		domain  string
		isValid bool
	}{
		{domain: "", isValid: false},
		{domain: "domain", isValid: false},
		{domain: "domain.com", isValid: false},
		{domain: ".domain.com", isValid: false},
		{domain: strings.Repeat("w", 64) + ".domain.com", isValid: false},
		{domain: "www.domain.com", isValid: false},
		{domain: "www.amocrm.any", isValid: false},
		{domain: "www.amocrm.ru", isValid: false},
		{domain: ".amocrm.ru", isValid: false},
		{domain: ".amocrm.", isValid: false},
		{domain: "any.amocrm.", isValid: false},
		{domain: "any.amocrm.ru", isValid: true},
		{domain: "any.amocrm.com", isValid: true},
	}

	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, redirectURL, testLogger)

	for _, tc := range cases {
		if tc.isValid {
			require.NoError(t, cl.SetDomain(ctx, tc.domain))
		} else {
			require.EqualError(t, cl.SetDomain(ctx, tc.domain), "invalid domain")
		}
	}
}
