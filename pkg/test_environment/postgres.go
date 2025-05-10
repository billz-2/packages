package test_environment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func SetupPostgres(ctx context.Context, cfg Config) (postgresConn *sqlx.DB, databaseUrl string, postgresContainer testcontainers.Container, err error) {
	internalPort := 5432
	exposedPort, err := GetFreePort()
	if err != nil {
		return nil, "", nil, err
	}

	var conStr string
	if cfg.Environment != "local" {
		// Increase timeout and polling interval for more stability
		ws := wait.NewHostPortStrategy(nat.Port(fmt.Sprintf("%d/tcp", internalPort))).
			WithPollInterval(1000 * time.Millisecond).
			WithStartupTimeout(20 * time.Minute)
		database := cfg.PostgresDatabase
		user := cfg.PostgresUser
		password := cfg.PostgresPassword

		req := testcontainers.ContainerRequest{
			Image:      "postgis/postgis:14-3.2-alpine",
			Entrypoint: nil,
			Env: map[string]string{
				"POSTGRES_DB":       database,
				"POSTGRES_USER":     user,
				"POSTGRES_PASSWORD": password,
				"TZ":                "UTC",
			},
			ExposedPorts: []string{fmt.Sprintf("%d:%d/tcp", exposedPort, internalPort)},
			WaitingFor:   ws,
			Name:         uuid.NewString(),
			User:         user,
			AutoRemove:   true,
		}

		postgresContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			return nil, "", nil, err
		}

		// Add retry logic for container endpoint
		var endpoint string
		maxRetries := 8
		for i := 0; i < maxRetries; i++ {
			endpoint, err = postgresContainer.Endpoint(ctx, "")
			if err == nil {
				break
			}

			if i == maxRetries-1 {
				return nil, "", nil, fmt.Errorf("failed to get endpoint after %d retries: %w", maxRetries, err)
			}

			time.Sleep(time.Second * 10)
		}

		host := getHost(endpoint)
		conStr = getDatabaseConnectionString(
			exposedPort,
			host,
			user,
			password,
			database,
		)
		databaseUrl = getDatabaseUrl(exposedPort, host, user, password, database)
	} else {
		conStr = fmt.Sprintf("host=%s port=%v user=%s password=%s dbname=%s sslmode=%s",
			cfg.PostgresHost,
			cfg.PostgresPort,
			cfg.PostgresUser,
			cfg.PostgresPassword,
			cfg.PostgresDatabase,
			"disable",
		)
		databaseUrl = getDatabaseUrl(cfg.PostgresPort, cfg.PostgresHost, cfg.PostgresUser, cfg.PostgresPassword, cfg.PostgresDatabase)
	}

	// Add retry logic for database connection
	maxRetries := 8
	for i := 0; i < maxRetries; i++ {
		postgresConn, err = sqlx.Open("postgres", conStr)
		if err == nil {
			// Test the connection
			err = postgresConn.Ping()
			if err == nil {
				break
			}
		}

		if i == maxRetries-1 {
			if postgresContainer != nil {
				_ = postgresContainer.Terminate(ctx)
			}
			return nil, "", nil, fmt.Errorf("failed to connect to database after %d retries: %w", maxRetries, err)
		}

		time.Sleep(time.Second * 10)
	}

	return postgresConn, databaseUrl, postgresContainer, nil
}

func getHost(e string) string {
	s := strings.Split(e, ":")
	return s[0]
}

func getDatabaseUrl(port int, host, user, password, database string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", user, password, host, port, database)
}
func getDatabaseConnectionString(port int, host, user, password, database string) string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host,
		port,
		user,
		password,
		database,
	)
}
