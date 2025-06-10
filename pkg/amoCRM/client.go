package amocrm

import (
	"context"
	"net/url"

	"github.com/billz-2/packages/pkg/logger"
)

// Client is a wrapper for authorization and making requests.
type Client interface {
	TokenByCode(ctx context.Context, code string) (Token, error)
	SetToken(ctx context.Context, token Token) error
	SetDomain(ctx context.Context, domain string) error
	Leads() Leads
}

// Verify interface compliance.
var _ Client = (*amoCRM)(nil)

type amoCRM struct {
	api *api
}

// New allocates and returns a new amoCRM API Client.
func New(clientID, clientSecret, redirectURL string) Client {
	return &amoCRM{
		api: newAPI(clientID, clientSecret, redirectURL),
	}
}

// SetToken stores given token to sign API requests.
func (a *amoCRM) SetToken(ctx context.Context, token Token) error {
	if token == nil {
		logger.Log.DebugWithCtx(ctx, "Setting token", logger.String("token_type", "nil"))
	} else {
		logger.Log.DebugWithCtx(ctx, "Setting token", logger.String("token_type", token.TokenType()))
	}
	return a.api.setToken(ctx, token)
}

// SetDomain stores given domain to build accounts-specific API endpoints.
func (a *amoCRM) SetDomain(ctx context.Context, domain string) error {
	logger.Log.DebugWithCtx(ctx, "Setting domain", logger.String("domain", domain))
	return a.api.setDomain(ctx, domain)
}

// TokenByCode makes a handshake with amoCRM, exchanging given
// authorization code for a set of tokens.
func (a *amoCRM) TokenByCode(ctx context.Context, code string) (Token, error) {
	logger.Log.DebugWithCtx(ctx, "Getting token by code")
	return a.api.getToken(ctx, authorizationCodeGrant, url.Values{
		"code":       []string{code},
		"grant_type": []string{"authorization_code"},
	}, nil)
}

func (a *amoCRM) Leads() Leads {
	return NewLead(a.api, logger.Log)
}
