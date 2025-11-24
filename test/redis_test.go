package test

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/billz-2/packages/pkg/test_environment"
	"github.com/stretchr/testify/require"
)

// Табличные интеграционные тесты для SetupRedis.
// Каждый кейс пытается поднять Redis в контейнере и проверяет, что выданный URI валиден и порт доступен по TCP.
func TestSetupRedis_Integration_Table(t *testing.T) {
	type testCase struct {
		name         string
		cfg          test_environment.Config
		expectScheme string
	}

	tests := []testCase{
		{
			name: "container_no_password",
			cfg: test_environment.Config{
				Environment:   "test",
				RedisPassword: "",
			},
			expectScheme: "redis",
		},
		{
			name: "container_with_password",
			cfg: test_environment.Config{
				Environment:   "test",
				RedisPassword: "secret-pass",
			},
			expectScheme: "redis",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			container, uri, err := test_environment.SetupRedis(testCtx, tc.cfg)
			if err != nil {
				t.Skipf("skipping %q: cannot start redis container via testcontainers: %v", tc.name, err)
				return
			}
			require.NotNil(t, container, "container should not be nil on success")
			t.Cleanup(func() {
				_ = container.Terminate(context.Background())
			})

			require.NotEmpty(t, uri, "uri must not be empty")

			parsed, parseErr := url.Parse(uri)
			require.NoError(t, parseErr, "uri must be parseable")
			require.Equal(t, tc.expectScheme, parsed.Scheme, "unexpected URI scheme")

			host := parsed.Hostname()
			port := parsed.Port()
			require.NotEmpty(t, host, "host must be present in URI")
			require.NotEmpty(t, port, "port must be present in URI")

			// Проверяем TCP-доступность
			conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
			require.NoErrorf(t, dialErr, "cannot connect to redis at %s", parsed.Host)
			if conn != nil {
				_ = conn.Close()
			}

			// Для случая с паролем убеждаемся, что креды не зашиты в URI.
			if tc.cfg.RedisPassword != "" {
				require.Nil(t, parsed.User, "credentials should not be embedded into URI")
			}
		})
	}
}
