package test_environment

import (
	"context"
	"fmt"
	"time"

	"github.com/billz-2/packages/pkg/logger"

	"github.com/docker/go-connections/nat"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func SetupElastic(ctx context.Context, cfg Config) (esConfig elasticsearch.Config, elastic testcontainers.Container, err error) {
	if cfg.Environment == "local" {
		return elasticsearch.Config{
			Addresses: cfg.ElasticSearchUrls,
			Username:  cfg.ElasticSearchUser,
			Password:  cfg.ElasticSearchPassword,
		}, elastic, nil
	}

	internalPort := "9200/tcp"

	ws := wait.ForHTTP("/_cluster/health").
		WithPort(nat.Port(internalPort)).
		WithStatusCodeMatcher(func(status int) bool {
			return status >= 200 && status < 300
		}).
		WithPollInterval(2 * time.Second).
		WithStartupTimeout(3 * time.Minute)

	req := testcontainers.ContainerRequest{
		Image: "venomuz/elastic_analysis-icu:9.0.4",
		Env: map[string]string{
			"discovery.type":                  "single-node",
			"ES_JAVA_OPTS":                    "-Xms512m -Xmx512m",
			"xpack.security.enabled":          "false",
			"xpack.security.http.ssl.enabled": "false",
		},
		ExposedPorts: []string{internalPort},
		WaitingFor:   ws,
	}

	elastic, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		logger.Log.Error("Failed to create container", logger.Error(err))
		return elasticsearch.Config{}, nil, fmt.Errorf("failed to create container: %w", err)
	}

	time.Sleep(2 * time.Second)

	mappedPort, err := elastic.MappedPort(ctx, nat.Port(internalPort))
	if err != nil {
		logger.Log.Error("Failed to get mapped port", logger.Error(err))
		return elasticsearch.Config{}, nil, fmt.Errorf("failed to get mapped port: %w", err)
	}

	ip, err := elastic.Host(ctx)
	if err != nil {
		logger.Log.Error("Failed to get host", logger.Error(err))
		return elasticsearch.Config{}, nil, fmt.Errorf("failed to get host: %w", err)
	}

	elasticAddress := fmt.Sprintf("http://%s:%s", ip, mappedPort.Port())

	logger.Log.Info("Elasticsearch container started successfully",
		logger.String("address", elasticAddress))

	return elasticsearch.Config{
		Addresses: []string{elasticAddress},
	}, elastic, nil
}
