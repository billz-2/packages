package amocrm

import (
	"github.com/billz-2/packages/pkg/logger"
)

// Client is a wrapper for authorization and making requests.
type Client interface {
	Leads() Leads
}

// Verify interface compliance.
var _ Client = (*amoCRM)(nil)

type amoCRM struct {
	api    *api
	logger logger.Logger
}

// New allocates and returns a new amoCRM API Client.
func New(clientID, clientSecret, token, redirectURL string, logger logger.Logger) Client {
	return &amoCRM{
		api:    newAPI(clientID, clientSecret, token, redirectURL, logger),
		logger: logger,
	}
}

func (a *amoCRM) Leads() Leads {
	return NewLead(a.api, a.logger)
}
