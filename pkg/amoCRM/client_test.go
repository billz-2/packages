package amocrm_test

import (
	"testing"

	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/stretchr/testify/require"
)

var (
	clientID     = "client_id"
	clientSecret = "client_secret"
	token        = "test_token"
	redirectURL  = "redirect_url"
	domain       = "domain"
)

func TestNew(t *testing.T) {
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, domain, testLogger)
	require.Implements(t, (*amocrm.Client)(nil), cl)
}

func TestAmoCRM_Leads(t *testing.T) {
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, domain, testLogger)

	leads := cl.Leads()
	require.NotNil(t, leads)
}
