package test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/billz-2/packages/pkg/test_environment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupMinio_LocalEnvironment(t *testing.T) {
	ctx := context.Background()

	cfg := test_environment.Config{
		Environment:    "local",
		MinioEndpoint:  "localhost:9000",
		MinioAccessKey: "minioadmin",
		MinioSecretKey: "minioadmin",
	}

	container, uri, err := test_environment.SetupMinio(ctx, cfg)

	require.NoError(t, err)
	assert.Nil(t, container)
	assert.Equal(t, cfg.MinioEndpoint, uri)
}

func TestSetupMinio_ContainerEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg := test_environment.Config{
		Environment:    "test",
		MinioAccessKey: "minioadmin",
		MinioSecretKey: "minioadmin",
	}

	container, uri, err := test_environment.SetupMinio(ctx, cfg)
	if err != nil {
		t.Skipf("cannot start minio container via testcontainers: %v", err)
	}
	require.NotNil(t, container)
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	require.NotEmpty(t, uri)
	assert.True(t, container.IsRunning())

	// uri должен быть в формате host:port
	host, port, splitErr := net.SplitHostPort(uri)
	require.NoError(t, splitErr, "uri must be in host:port format, got: %s", uri)
	require.NotEmpty(t, host)
	require.NotEmpty(t, port)

	// TCP-доступность
	conn, dialErr := net.DialTimeout("tcp", uri, 5*time.Second)
	require.NoErrorf(t, dialErr, "cannot connect to minio at %s", uri)
	_ = conn.Close()

	// HTTP health check
	healthURL := fmt.Sprintf("http://%s/minio/health/ready", uri)
	resp, httpErr := http.Get(healthURL) //nolint:noctx
	require.NoErrorf(t, httpErr, "health check request failed: %s", healthURL)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
