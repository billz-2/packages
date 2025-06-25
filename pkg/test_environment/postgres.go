package test_environment

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type PostgresContainer struct {
	Conn        *sqlx.DB
	Container   testcontainers.Container
	DatabaseUrl string
	ExposedPort string
	Network     *testcontainers.DockerNetwork
}

func SetupPostgres(ctx context.Context, cfg Config) (postgresConn *sqlx.DB, databaseUrl string, postgresContainer testcontainers.Container, err error) {
	network, err := CreateDockerNetwork(ctx)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to create Docker network: %w", err)
	}

	pgContainer, err := SetupPostgresV2(ctx, cfg, network)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to setup Postgres container: %w", err)
	}

	return pgContainer.Conn, pgContainer.DatabaseUrl, pgContainer.Container, nil
}

func SetupPostgresV2(ctx context.Context, cfg Config, network *testcontainers.DockerNetwork) (*PostgresContainer, error) {
	internalPort := 5432
	exposedPort, err := GetFreePort()
	if err != nil {
		return nil, err
	}

	var (
		postgresConn      *sqlx.DB
		postgresContainer testcontainers.Container
		databaseUrl       string
		conStr            string
	)
	port := cfg.PostgresPort

	if cfg.Environment != "local" {
		// Increase timeout and polling interval for more stability
		ws := wait.NewHostPortStrategy(nat.Port(fmt.Sprintf("%d/tcp", internalPort))).
			WithPollInterval(500 * time.Millisecond).
			WithStartupTimeout(10 * time.Minute)

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
			Networks:     []string{network.Name},
			User:         user,
			AutoRemove:   true,
			SkipReaper:   true,
		}

		postgresContainer, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			return nil, err
		}

		// Add retry logic for chContainer endpoint
		maxRetries := 8
		for i := 0; i < maxRetries; i++ {
			_, err = postgresContainer.Endpoint(ctx, "")
			if err == nil {
				break
			}

			if i == maxRetries-1 {
				return nil, fmt.Errorf("failed to get endpoint after %d retries: %w", maxRetries, err)
			}

			time.Sleep(time.Second * 10)
		}
		port = exposedPort
	}

	conStr = getDatabaseConnectionString(
		port,
		cfg.PostgresHost,
		cfg.PostgresUser,
		cfg.PostgresPassword,
		cfg.PostgresDatabase,
	)
	databaseUrl = getDatabaseUrl(
		port,
		cfg.PostgresHost,
		cfg.PostgresUser,
		cfg.PostgresPassword,
		cfg.PostgresDatabase,
	)

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
				if postgresContainer.IsRunning() {
					_ = postgresContainer.Terminate(ctx)
				}
			}

			return nil, fmt.Errorf("failed to connect to database after %d retries: %w", maxRetries, err)
		}

		time.Sleep(time.Second * 5)
	}

	return &PostgresContainer{
		Conn:        postgresConn,
		Container:   postgresContainer,
		DatabaseUrl: databaseUrl,
		ExposedPort: strconv.Itoa(exposedPort),
		Network:     network,
	}, nil
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
