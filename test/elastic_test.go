package test

import (
	"context"
	"testing"
	"time"

	"github.com/billz-2/packages/pkg/test_environment"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupElastic_LocalEnvironment(t *testing.T) {
	ctx := context.Background()

	cfg := test_environment.Config{
		Environment:           "local",
		ElasticSearchUrls:     []string{"http://localhost:9200"},
		ElasticSearchUser:     "testuser",
		ElasticSearchPassword: "testpass",
	}

	esConfig, container, err := test_environment.SetupElastic(ctx, cfg)

	require.NoError(t, err)
	assert.Nil(t, container)
	assert.Equal(t, cfg.ElasticSearchUrls, esConfig.Addresses)
	assert.Equal(t, cfg.ElasticSearchUser, esConfig.Username)
	assert.Equal(t, cfg.ElasticSearchPassword, esConfig.Password)
}

func TestSetupElastic_ContainerEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg := test_environment.Config{
		Environment: "test",
	}

	esConfig, container, err := test_environment.SetupElastic(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, container)

	defer func() {
		if container != nil {
			_ = container.Terminate(context.Background())
		}
	}()

	assert.NotEmpty(t, esConfig.Addresses)
	assert.Equal(t, "elastic", esConfig.Username)
	assert.Equal(t, "elastic", esConfig.Password)
	assert.Contains(t, esConfig.Addresses[0], "http://")

	isRunning := container.IsRunning()
	assert.True(t, isRunning)

	client, err := elasticsearch.NewClient(esConfig)
	require.NoError(t, err)

	res, err := client.Info()
	require.NoError(t, err)
	defer res.Body.Close()
	assert.False(t, res.IsError())
}

func TestSetupElastic_ContainerEnvironment_PluginInstalled(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg := test_environment.Config{
		Environment: "test",
	}

	_, container, err := test_environment.SetupElastic(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, container)

	defer func() {
		if container != nil {
			_ = container.Terminate(context.Background())
		}
	}()

	exitCode, reader, err := container.Exec(ctx, []string{
		"elasticsearch-plugin", "list",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)

	buf := make([]byte, 1024)
	n, _ := reader.Read(buf)
	output := string(buf[:n])
	assert.Contains(t, output, "analysis-icu")
}

func TestSetupElastic_MultipleEnvironments(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		expectLocal bool
	}{
		{
			name:        "local environment",
			environment: "local",
			expectLocal: true,
		},
		{
			name:        "development environment",
			environment: "dev",
			expectLocal: false,
		},
		{
			name:        "test environment",
			environment: "test",
			expectLocal: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.expectLocal && testing.Short() {
				t.Skip("Skipping integration test in short mode")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			cfg := test_environment.Config{
				Environment:           tt.environment,
				ElasticSearchUrls:     []string{"http://localhost:9200"},
				ElasticSearchUser:     "testuser",
				ElasticSearchPassword: "testpass",
			}

			esConfig, container, err := test_environment.SetupElastic(ctx, cfg)
			require.NoError(t, err)

			if tt.expectLocal {
				assert.Nil(t, container)
			} else {
				assert.NotNil(t, container)
				defer func() {
					if container != nil {
						_ = container.Terminate(context.Background())
					}
				}()
			}

			assert.NotEmpty(t, esConfig.Addresses)
		})
	}
}
