package amocrm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/billz-2/packages/pkg/logger"
)

type GrantType struct {
	code   string
	fields []string
}

var (
	authorizationCodeGrant = GrantType{
		code:   "authorization_code",
		fields: []string{"code"},
	}
	refreshTokenGrant = GrantType{
		code:   "refresh_token",
		fields: []string{"refresh_token"},
	}
)

const (
	userAgent       = "AmoCRM-API-Golang-Client"
	apiVersion      = uint8(4)
	requestTimeout  = 20 * time.Second
	BearerTokenType = "Bearer"
)

// api implements Client interface.
type api struct {
	clientID     string
	clientSecret string
	redirectURL  string

	domain string
	token  string

	http   *http.Client
	logger logger.Logger
}

func newAPI(clientID, clientSecret, token, redirectURL, domain string, logger logger.Logger) *api {
	return &api{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURL:  redirectURL,
		http: &http.Client{
			Timeout: requestTimeout,
		},
		logger: logger,
		token:  token,
		domain: domain,
	}
}

func (a *api) get(ctx context.Context, ep endpoint, q url.Values, h http.Header) (*http.Response, error) {
	a.logger.DebugWithCtx(ctx, "Making GET request", logger.String("endpoint", string(ep)))

	header := a.header()
	for k, v := range h {
		if _, reserved := header[k]; !reserved {
			header[k] = v
		}
	}

	apiURL, err := a.url(ep.path(), q)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to build URL", logger.String("endpoint", string(ep)), logger.Error(err))
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL.String(), nil)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to create request", logger.String("endpoint", string(ep)), logger.Error(err))
		return nil, err
	}
	req.Header = header

	resp, err := a.http.Do(req)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Request failed", logger.String("endpoint", string(ep)), logger.Error(err))
	} else {
		a.logger.DebugWithCtx(ctx, "Request successful", logger.String("endpoint", string(ep)), logger.Int("status", resp.StatusCode))
	}
	return resp, err
}

func (a *api) patch(ctx context.Context, ep endpoint, q url.Values, h http.Header, body io.Reader) (*http.Response, error) {
	a.logger.DebugWithCtx(ctx, "Making PATCH request", logger.String("endpoint", string(ep)))

	header := a.header()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Content-Type", "application/json")
	for k, v := range h {
		if _, reserved := header[k]; !reserved {
			header[k] = v
		}
	}

	apiURL, err := a.url(ep.path(), q)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to build URL", logger.String("endpoint", string(ep)), logger.Error(err))
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, apiURL.String(), io.NopCloser(body))
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to create request", logger.String("endpoint", string(ep)), logger.Error(err))
		return nil, err
	}
	req.Header = header

	resp, err := a.http.Do(req)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Request failed", logger.String("endpoint", string(ep)), logger.Error(err))
	} else {
		a.logger.DebugWithCtx(ctx, "Request successful", logger.String("endpoint", string(ep)), logger.Int("status", resp.StatusCode))
	}
	return resp, err
}

func (a *api) url(path string, q url.Values) (*url.URL, error) {
	if !isValidDomain(a.domain) {
		return nil, amoCrmApiErrWrap("invalid accounts domain")
	}

	endpointURL := a.domain + path + "?" + q.Encode()

	return url.Parse(endpointURL)
}

func (a *api) header() http.Header {
	authHeader := BearerTokenType + " " + a.token
	header := a.baseHeader()
	header["Authorization"] = []string{authHeader}

	return header
}

func (a *api) baseHeader() http.Header {
	return http.Header{
		"User-Agent": []string{userAgent},
	}
}

func isValidDomain(domain string) bool {
	if domain == "" {
		return false
	}

	parts := strings.Split(domain, ".")
	if len(parts) != 3 ||
		parts[0] == "" ||
		parts[0] == "www" ||
		len(parts[0]) > 63 ||
		parts[1] != "amocrm" ||
		parts[2] != "ru" && parts[2] != "com" {
		return false
	}

	return true
}

func amoCrmApiErrWrap(format string, args ...any) error {
	return fmt.Errorf("amoCRM api client error: "+format, args...)
}
