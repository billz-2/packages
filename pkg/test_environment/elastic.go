package test_environment

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/docker/go-connections/nat"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// EnvElasticsearchImage is the name of the environment variable that
// callers may set to override the default Elasticsearch image used by
// SetupElastic. This is useful when a service needs an image with extra
// plugins installed (e.g. analysis-icu), but does not want to fork the
// whole pkg/test_environment.
const EnvElasticsearchImage = "TEST_ELASTICSEARCH_IMAGE"

// defaultElasticsearchImage is the image used when EnvElasticsearchImage
// is not set. Keep it in sync with the Elasticsearch version the package
// has historically targeted, so existing callers don't change behavior.
const defaultElasticsearchImage = "docker.elastic.co/elasticsearch/elasticsearch:9.0.4"

func elasticsearchImage() string {
	if v := os.Getenv(EnvElasticsearchImage); v != "" {
		return v
	}
	return defaultElasticsearchImage
}

func SetupElastic(ctx context.Context, cfg Config) (esConfig elasticsearch.Config, elastic testcontainers.Container, err error) {
	if cfg.Environment == "local" {
		return elasticsearch.Config{
			Addresses: cfg.ElasticSearchUrls,
			Username:  cfg.ElasticSearchUser,
			Password:  cfg.ElasticSearchPassword,
		}, elastic, nil
	}

	exposedPort, err := GetFreePort()
	if err != nil {
		logger.Log.Error("Could not get free port", logger.Error(err))
		return esConfig, nil, err
	}

	internalPort := 9200

	ws := wait.ForHTTP("/").
		WithPort(nat.Port(fmt.Sprintf("%d/tcp", internalPort))).
		WithPollInterval(2 * time.Second).
		WithStartupTimeout(5 * time.Minute)

	req := testcontainers.ContainerRequest{
		Image: elasticsearchImage(),
		Env: map[string]string{
			"discovery.type":                  "single-node",
			"ES_JAVA_OPTS":                    "-Xms512m -Xmx512m",
			"xpack.security.enabled":          "false",
			"xpack.security.http.ssl.enabled": "false",
			"http.host":                       "0.0.0.0",
		},
		ExposedPorts: []string{fmt.Sprintf("%d:%d/tcp", exposedPort, internalPort)},
		WaitingFor:   ws,
	}

	elastic, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return elasticsearch.Config{}, nil, err
	}

	logger.Log.Info("Installing analysis-icu plugin...")
	exitCode, output, err := elastic.Exec(ctx, []string{"elasticsearch-plugin", "install", "analysis-icu"})
	if err != nil || exitCode != 0 {
		logger.Log.Error("Failed to install plugin", logger.Error(err), logger.Any("output", output))
		return elasticsearch.Config{}, nil, fmt.Errorf("failed to install plugin: %v, output: %s", err, output)
	}
	logger.Log.Info("Plugin installed successfully")

	logger.Log.Info("Restarting Elasticsearch...")
	if err := elastic.Stop(ctx, nil); err != nil {
		logger.Log.Error("Failed to stop container", logger.Error(err))
		return elasticsearch.Config{}, nil, err
	}

	if err := elastic.Start(ctx); err != nil {
		logger.Log.Error("Failed to start container", logger.Error(err))
		return elasticsearch.Config{}, nil, err
	}

	logger.Log.Info("Waiting for Elasticsearch to be ready...")

	ip, err := elastic.Host(ctx)
	if err != nil {
		return elasticsearch.Config{}, nil, err
	}

	elasticAddress := fmt.Sprintf("http://%s:%d", ip, exposedPort)
	return elasticsearch.Config{
		Addresses: []string{elasticAddress},
		Username:  "elastic",
		Password:  "elastic",
	}, elastic, nil
}
