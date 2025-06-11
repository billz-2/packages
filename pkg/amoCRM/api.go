package amocrm

import (
	"context"
	"encoding/json"
	"errors"
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
	userAgent      = "AmoCRM-API-Golang-Client"
	apiVersion     = uint8(4)
	requestTimeout = 20 * time.Second
)

// api implements Client interface.
type api struct {
	clientID     string
	clientSecret string
	redirectURL  string

	domain string
	token  Token

	http   *http.Client
	logger logger.Logger
}

func newAPI(clientID, clientSecret, redirectURL string, logger logger.Logger) *api {
	return &api{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURL:  redirectURL,
		http: &http.Client{
			Timeout: requestTimeout,
		},
		logger: logger,
	}
}

func (a *api) get(ctx context.Context, ep endpoint, q url.Values, h http.Header) (*http.Response, error) {
	a.logger.DebugWithCtx(ctx, "Making GET request", logger.String("endpoint", string(ep)))

	err := a.checkToken(ctx)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to check token", logger.String("endpoint", string(ep)), logger.Error(err))
		return nil, err
	}

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

	err := a.checkToken(ctx)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to check token", logger.String("endpoint", string(ep)), logger.Error(err))
		return nil, err
	}

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

func (a *api) setToken(ctx context.Context, token Token) error {
	if token == nil {
		a.logger.ErrorWithCtx(ctx, "Invalid token")
		return errors.New("invalid token")
	}

	a.logger.DebugWithCtx(ctx, "Setting token", logger.String("token_type", token.TokenType()))
	a.token = token
	return nil
}

func (a *api) setDomain(ctx context.Context, domain string) error {
	a.logger.DebugWithCtx(ctx, "Setting domain", logger.String("domain", domain))

	if !isValidDomain(domain) {
		a.logger.ErrorWithCtx(ctx, "Invalid domain", logger.String("domain", domain))
		return errors.New("invalid domain")
	}

	a.domain = domain
	return nil
}

func (a *api) getToken(ctx context.Context, grant GrantType, options url.Values, header http.Header) (Token, error) {
	a.logger.DebugWithCtx(ctx, "Getting token", logger.String("grant_type", grant.code))

	if !isValidDomain(a.domain) {
		a.logger.ErrorWithCtx(ctx, "Invalid accounts domain", logger.String("domain", a.domain))
		return nil, amoCrmApiErrWrap("invalid accounts domain")
	}

	// Validate required grantType-specific fields
	for _, key := range grant.fields {
		if values, ok := options[key]; len(values) == 0 || !ok {
			a.logger.ErrorWithCtx(ctx, "Missing required grant parameter",
				logger.String("grant_type", grant.code),
				logger.String("parameter", key))
			return nil, amoCrmApiErrWrap("missing required %s grant parameter %s", grant.code, key)
		}
	}

	// Default request parameters
	data := url.Values{
		"client_id":     []string{a.clientID},
		"client_secret": []string{a.clientSecret},
		"redirect_uri":  []string{a.redirectURL},
		"grant_type":    []string{grant.code},
	}

	// Merge options with default parameters
	for k, v := range options {
		if _, reserved := data[k]; !reserved {
			data[k] = v
		}
	}

	// Set request URL
	tokenURL, err := a.url("/oauth2/access_token", nil)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to build request URL", logger.Error(err))
		return nil, amoCrmApiErrWrap("build request url")
	}

	// Set request headers
	reqHeader := a.baseHeader()
	reqHeader["Content-Type"] = []string{"application/x-www-form-urlencoded"}
	for k, v := range header {
		if _, reserved := reqHeader[k]; !reserved {
			reqHeader[k] = v
		}
	}

	// Create request body
	reqBody := io.NopCloser(strings.NewReader(data.Encode()))

	// Build request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL.String(), reqBody)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to create request", logger.Error(err))
		return nil, amoCrmApiErrWrap("create request")
	}
	req.Header = reqHeader

	a.logger.DebugWithCtx(ctx, "Sending token request", logger.String("url", tokenURL.String()))
	resp, err := a.http.Do(req)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to send request", logger.Error(err))
		return nil, amoCrmApiErrWrap("send request")
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if closeBodyErr := resp.Body.Close(); closeBodyErr != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to close response body", logger.Error(closeBodyErr))
		return nil, amoCrmApiErrWrap("close response body")
	}
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to read response body", logger.Error(err))
		return nil, amoCrmApiErrWrap("fetch response body")
	}

	if statusCode := resp.StatusCode; statusCode < 200 || statusCode > 299 {
		a.logger.ErrorWithCtx(ctx, "Unexpected status code",
			logger.Int("status_code", statusCode),
			logger.String("response", string(respBody)))
		return nil, amoCrmApiErrWrap("fetch token: response: %v - %s, request: %+v", resp.Status, respBody, req)
	}

	var jsonToken tokenJSON
	if err = json.Unmarshal(respBody, &jsonToken); err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to parse token from JSON", logger.Error(err))
		return nil, amoCrmApiErrWrap("parse token from json")
	}

	token := &tokenSource{
		accessToken:  jsonToken.AccessToken,
		tokenType:    jsonToken.TokenType,
		refreshToken: jsonToken.RefreshToken,
		expiresAt:    time.Now().Add(time.Duration(jsonToken.ExpiresIn) * time.Second),
	}

	if token.accessToken == "" {
		a.logger.ErrorWithCtx(ctx, "Server response missing access_token")
		return nil, amoCrmApiErrWrap("server response missing access_token")
	}

	a.logger.DebugWithCtx(ctx, "Token obtained successfully")
	return token, nil
}

func (a *api) refreshToken(ctx context.Context) error {
	a.logger.DebugWithCtx(ctx, "Refreshing token")

	if a.token.RefreshToken() == "" {
		a.logger.ErrorWithCtx(ctx, "Empty refresh token")
		return amoCrmApiErrWrap("empty refresh token")
	}

	token, err := a.getToken(ctx, refreshTokenGrant, url.Values{
		"grant_type":    []string{"refresh_token"},
		"refresh_token": []string{a.token.RefreshToken()},
	}, nil)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "Failed to refresh token", logger.Error(err))
		return err
	}

	a.logger.DebugWithCtx(ctx, "Token refreshed successfully")
	a.token = token
	return nil
}

func (a *api) url(path string, q url.Values) (*url.URL, error) {
	if !isValidDomain(a.domain) {
		return nil, amoCrmApiErrWrap("invalid accounts domain")
	}

	endpointURL := "https://" + a.domain + path + "?" + q.Encode()

	return url.Parse(endpointURL)
}

func (a *api) header() http.Header {
	authHeader := a.token.TokenType() + " " + a.token.AccessToken()

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

func (a *api) checkToken(ctx context.Context) error {
	a.logger.DebugWithCtx(ctx, "Checking token")

	if a.token == nil {
		a.logger.ErrorWithCtx(ctx, "Invalid token")
		return errors.New("invalid token")
	}

	if a.token.Expired() {
		a.logger.DebugWithCtx(ctx, "Token expired, refreshing")
		if err := a.refreshToken(ctx); err != nil {
			a.logger.ErrorWithCtx(ctx, "Failed to refresh token", logger.Error(err))
			return err
		}
	}

	return nil
}
