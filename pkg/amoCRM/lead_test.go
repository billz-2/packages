package amocrm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func mockTransport(t *testing.T, responder roundTripFunc) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = responder
	t.Cleanup(func() { http.DefaultTransport = original })
}

func TestBatchUpdateEmptySlice(t *testing.T) {
	// Create a client
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, domain, testLogger)

	// Call BatchUpdate with empty slice
	err := cl.Leads().BatchUpdate(ctx, []*amocrm.LeadUpdate{})
	require.NoError(t, err)
}

func TestUpdateNilLead(t *testing.T) {
	// Create a client
	testLogger := logger.New(logger.LevelDebug, "test")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, domain, testLogger)

	// Call Update with nil lead
	err := cl.Leads().Update(ctx, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "lead is nil")
}

func TestLeadUpdateWithValidData(t *testing.T) {
	name := "Test Lead"
	status := 142
	lead := &amocrm.LeadUpdate{ID: 12345, Name: &name, StatusID: &status}

	for _, tc := range []struct {
		domain string
		host   string
	}{
		{domain: "billz.amocrm.ru", host: "billz.amocrm.ru"},
		{domain: "billz.amocrm.ru/", host: "billz.amocrm.ru"},
		{domain: "https://billz.amocrm.ru/", host: "billz.amocrm.ru"},
		{domain: "http://billz.amocrm.ru/", host: "billz.amocrm.ru"},
		{domain: "https://billz.amocrm.com/", host: "billz.amocrm.com"},
		{domain: "  HTTPS://BILLZ.AMOCRM.RU/  ", host: "billz.amocrm.ru"},
		{domain: "https://shop-42.amocrm.ru/", host: "shop-42.amocrm.ru"},
	} {
		t.Run(tc.domain, func(t *testing.T) {
			calls := 0
			mockTransport(t, func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, http.MethodPatch, req.Method)
				require.Equal(t, "https://"+tc.host+"/api/v4/leads/12345", req.URL.String())
				require.Equal(t, "Bearer "+token, req.Header.Get("Authorization"))
				require.Equal(t, "application/json", req.Header.Get("Content-Type"))

				var got amocrm.LeadUpdate
				require.NoError(t, json.NewDecoder(req.Body).Decode(&got))
				require.Equal(t, *lead, got)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("{}")),
				}, nil
			})

			testLogger := logger.New(logger.LevelDebug, "test-lead-update")
			cl := amocrm.New(clientID, clientSecret, token, redirectURL, tc.domain, testLogger)
			require.NoError(t, cl.Leads().Update(ctx, lead))
			require.Equal(t, 1, calls)
		})
	}
}

func TestBatchUpdateWithValidData(t *testing.T) {
	name1 := "Test Lead 1"
	name2 := "Test Lead 2"
	leads := []*amocrm.LeadUpdate{
		{ID: 1, Name: &name1},
		{ID: 2, Name: &name2},
	}

	calls := 0
	mockTransport(t, func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodPatch, req.Method)
		require.Equal(t, "https://billz.amocrm.ru/api/v4/leads", req.URL.String())
		require.Equal(t, "Bearer "+token, req.Header.Get("Authorization"))
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))

		var got []*amocrm.LeadUpdate
		require.NoError(t, json.NewDecoder(req.Body).Decode(&got))
		require.Equal(t, leads, got)
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})

	testLogger := logger.New(logger.LevelDebug, "test-batch-update")
	cl := amocrm.New(clientID, clientSecret, token, redirectURL, domain, testLogger)
	require.NoError(t, cl.Leads().BatchUpdate(ctx, leads))
	require.Equal(t, 1, calls)
}

func TestLeadUpdateRejectsInvalidDomainBeforeHTTP(t *testing.T) {
	for _, invalidDomain := range []string{
		"",
		"https://",
		"www.amocrm.ru",
		"billz.example.com",
		"https://billz.amocrm.ru/path",
		"https://billz.amocrm.ru?query=value",
		"https://user:pass@billz.amocrm.ru/",
		"https://http://billz.amocrm.ru/",
		"bad_name.amocrm.ru",
		"-bad.amocrm.ru",
		"bad-.amocrm.ru",
	} {
		t.Run(invalidDomain, func(t *testing.T) {
			calls := 0
			mockTransport(t, func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected HTTP request")
			})

			testLogger := logger.New(logger.LevelDebug, "test-invalid-domain")
			cl := amocrm.New(clientID, clientSecret, token, redirectURL, invalidDomain, testLogger)
			err := cl.Leads().Update(ctx, &amocrm.LeadUpdate{ID: 12345})
			require.ErrorContains(t, err, "invalid accounts domain")
			require.Zero(t, calls)
		})
	}
}
