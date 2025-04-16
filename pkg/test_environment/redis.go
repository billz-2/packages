package test_environment

import (
	"context"
	"fmt"
	"time"

	"github.com/billz-2/packages/pkg/logger"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func SetupRedis(ctx context.Context, cfg Config) (
	container testcontainers.Container, uri string, err error,
) {
	if cfg.Environment == "local" {
		return nil, fmt.Sprintf("redis://%s:%s", cfg.RedisAddress, cfg.RedisPort), nil
	}

	internalPort := 6379
	exposedPort, err := GetFreePort()
	if err != nil {
		logger.Log.Error("Could not get free port", logger.Error(err))
		return nil, "", err
	}
	ws := wait.NewHostPortStrategy(nat.Port(fmt.Sprintf("%d/tcp", internalPort))).
		WithPollInterval(100 * time.Millisecond).
		WithStartupTimeout(5 * time.Minute)

	req := testcontainers.ContainerRequest{
		Image:        "bitnami/redis:latest",
		ExposedPorts: []string{fmt.Sprintf("%d:%d/tcp", exposedPort, internalPort)},
		Env: map[string]string{
			"ALLOW_EMPTY_PASSWORD": "yes",
		},
		WaitingFor: ws,
	}
	container, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return container, uri, err
	}

	endpoint, err := container.Endpoint(ctx, "")
	if err != nil {
		return container, uri, err
	}

	host := getHost(endpoint)

	uri = fmt.Sprintf("redis://%s:%d", host, exposedPort)

	return container, uri, nil
}
