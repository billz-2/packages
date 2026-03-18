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

func SetupMinio(ctx context.Context, cfg Config) (
	container testcontainers.Container, uri string, err error,
) {
	if cfg.Environment == "local" {
		return nil, cfg.MinioEndpoint, nil
	}

	exposedPort, err := GetFreePort()
	if err != nil {
		logger.Log.Error("Could not get free port", logger.Error(err))
		return nil, "", err
	}

	internalPort := 9000
	ws := wait.NewHostPortStrategy(nat.Port(fmt.Sprintf("%d/tcp", internalPort))).
		WithPollInterval(100 * time.Millisecond).
		WithStartupTimeout(5 * time.Minute)

	cmd := []string{"server", "/data"}
	req := testcontainers.ContainerRequest{
		Image:        "minio/minio:RELEASE.2025-07-18T21-56-31Z",
		ExposedPorts: []string{fmt.Sprintf("%d:%d/tcp", exposedPort, internalPort)},
		Env: map[string]string{
			"MINIO_ACCESS_KEY": cfg.MinioAccessKey,
			"MINIO_SECRET_KEY": cfg.MinioSecretKey,
		},
		Cmd:        cmd,
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

	uri = fmt.Sprintf("%s:%d", host, exposedPort)

	return container, uri, nil
}
