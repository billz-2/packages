package test_environment

import (
	"context"
	"fmt"
	"time"

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

	ws := wait.ForLog("started").
		WithPollInterval(1 * time.Second).
		WithStartupTimeout(5 * time.Minute)

	req := testcontainers.ContainerRequest{
		Image: "venomuz/elastic_analysis-icu:9.0.4",
		// Remove Name field to avoid conflicts
		Env: map[string]string{
			"discovery.type":                  "single-node",
			"ES_JAVA_OPTS":                    "-Xms512m -Xmx512m",
			"xpack.security.enabled":          "false",
			"xpack.security.http.ssl.enabled": "false",
		},
		ExposedPorts: []string{internalPort},
		WaitingFor:   ws,
		// Remove AutoRemove in test context to allow proper cleanup
	}

	elastic, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return elasticsearch.Config{}, nil, fmt.Errorf("failed to create container: %w", err)
	}

	// Get the mapped port
	mappedPort, err := elastic.MappedPort(ctx, nat.Port(internalPort))
	if err != nil {
		return elasticsearch.Config{}, nil, fmt.Errorf("failed to get mapped port: %w", err)
	}

	ip, err := elastic.Host(ctx)
	if err != nil {
		return elasticsearch.Config{}, nil, fmt.Errorf("failed to get host: %w", err)
	}

	elasticAddress := fmt.Sprintf("http://%s:%s", ip, mappedPort.Port())

	return elasticsearch.Config{
		Addresses: []string{elasticAddress},
		Username:  "elastic",
		Password:  "elastic",
	}, elastic, nil
}
