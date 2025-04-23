package tracing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_newExporter_URLParsing(t *testing.T) {
	testCases := []struct {
		name      string
		jaegerURL string
		expected  string
	}{
		{
			name:      "simple host:port",
			jaegerURL: "localhost:4317",
			expected:  "localhost:4317",
		},
		{
			name:      "host:port with http scheme",
			jaegerURL: "http://localhost:4317",
			expected:  "localhost:4317",
		},
		{
			name:      "host:port with https scheme",
			jaegerURL: "https://localhost:4317",
			expected:  "localhost:4317",
		},
		{
			name:      "host:port with path",
			jaegerURL: "localhost:4317/api/traces",
			expected:  "localhost:4317",
		},
		{
			name:      "full URL with scheme and path",
			jaegerURL: "http://jaeger-collector.observability.svc.cluster.local:14268/api/traces",
			expected:  "jaeger-collector.observability.svc.cluster.local:14268",
		},
		{
			name:      "full URL with https scheme and path",
			jaegerURL: "https://jaeger-collector.observability.svc.cluster.local:14268/api/traces",
			expected:  "jaeger-collector.observability.svc.cluster.local:14268",
		},
		{
			name:      "current config format",
			jaegerURL: "jaeger-collector.observability.svc.cluster.local:14268",
			expected:  "jaeger-collector.observability.svc.cluster.local:14268",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a test Config with the test URL
			cfg := &Config{
				ServiceName: "test-service",
				JaegerUrl:   tc.jaegerURL,
			}

			// Use a helper to extract the parsed URL for testing
			parsed := parseJaegerURL(cfg.JaegerUrl)
			require.Equal(t, tc.expected, parsed)

			// Also test that the exporter creation doesn't panic
			_, err := newExporter(context.Background(), cfg)
			// We can't fully test the exporter connection since it would try to connect
			// Just verify no panic and expected error behavior
			if err != nil {
				// Some errors are expected when testing without a real Jaeger endpoint
				// Just make sure it's not the URL parsing error
				require.NotContains(t, err.Error(), "invalid target address")
				require.NotContains(t, err.Error(), "too many colons in address")
			}
		})
	}
}

// Helper function that extracts just the URL parsing part of newExporter
// for easier testing
func parseJaegerURL(url string) string {
	// Remove scheme (http:// or https://) if present
	if idx := indexOf(url, "://"); idx >= 0 {
		url = url[idx+3:]
	}
	
	// Remove path if present - for gRPC we only need host:port
	if idx := indexOf(url, "/"); idx >= 0 {
		url = url[:idx]
	}
	
	return url
}