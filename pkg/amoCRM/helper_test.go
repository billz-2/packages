package amocrm_test

import (
	"testing"

	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	"github.com/stretchr/testify/require"
)

func TestGetLeadIDFromURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		expectedID  int
		expectError bool
	}{
		{
			name:        "valid AmoCRM URL with detail",
			url:         "https://billz.amocrm.ru/leads/detail/27207417",
			expectedID:  27207417,
			expectError: false,
		},
		{
			name:        "valid AmoCRM URL with different domain",
			url:         "https://example.amocrm.ru/leads/detail/12345",
			expectedID:  12345,
			expectError: false,
		},
		{
			name:        "valid AmoCRM URL with http",
			url:         "http://test.amocrm.ru/leads/detail/999",
			expectedID:  999,
			expectError: false,
		},
		{
			name:        "URL with trailing slash",
			url:         "https://billz.amocrm.ru/leads/detail/27207417/",
			expectedID:  27207417,
			expectError: false,
		},
		{
			name:        "URL with query parameters",
			url:         "https://billz.amocrm.ru/leads/detail/27207417?tab=notes",
			expectedID:  27207417,
			expectError: false,
		},
		{
			name:        "URL with fragment",
			url:         "https://billz.amocrm.ru/leads/detail/27207417#section",
			expectedID:  27207417,
			expectError: false,
		},
		{
			name:        "fallback to last segment",
			url:         "https://billz.amocrm.ru/some/path/54321",
			expectedID:  54321,
			expectError: false,
		},
		{
			name:        "empty URL",
			url:         "",
			expectedID:  0,
			expectError: true,
		},
		{
			name:        "invalid URL",
			url:         "not-a-url",
			expectedID:  0,
			expectError: true,
		},
		{
			name:        "URL without numeric ID",
			url:         "https://billz.amocrm.ru/leads/detail/invalid",
			expectedID:  0,
			expectError: true,
		},
		{
			name:        "URL too short",
			url:         "https://billz.amocrm.ru/leads",
			expectedID:  0,
			expectError: true,
		},
		{
			name:        "URL with non-numeric last segment",
			url:         "https://billz.amocrm.ru/some/path/text",
			expectedID:  0,
			expectError: true,
		},
		{
			name:        "valid URL with large ID",
			url:         "https://billz.amocrm.ru/leads/detail/2147483647",
			expectedID:  2147483647,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := amocrm.GetLeadIDFromURL(tt.url)
			
			if tt.expectError {
				require.Error(t, err)
				require.Equal(t, 0, id)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedID, id)
			}
		})
	}
}

func TestGetLeadIDFromURL_EdgeCases(t *testing.T) {
	t.Run("URL with multiple leads segments", func(t *testing.T) {
		url := "https://billz.amocrm.ru/leads/leads/detail/12345"
		id, err := amocrm.GetLeadIDFromURL(url)
		require.NoError(t, err)
		require.Equal(t, 12345, id)
	})

	t.Run("URL with leads but no detail", func(t *testing.T) {
		url := "https://billz.amocrm.ru/leads/list/12345"
		id, err := amocrm.GetLeadIDFromURL(url)
		require.NoError(t, err)
		require.Equal(t, 12345, id) // Should fallback to last segment
	})

	t.Run("URL with zero ID", func(t *testing.T) {
		url := "https://billz.amocrm.ru/leads/detail/0"
		id, err := amocrm.GetLeadIDFromURL(url)
		require.NoError(t, err)
		require.Equal(t, 0, id)
	})
}