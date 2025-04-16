package test_environment

import (
	"context"
	"fmt"
	"time"

	"github.com/billz-2/packages/pkg/logger"

	"github.com/docker/go-connections/nat"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/google/uuid"
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

	exposedPort, err := GetFreePort()
	if err != nil {
		logger.Log.Error("Could not get free port", logger.Error(err))
		return esConfig, nil, err
	}

	internalPort := 9200
	ws := wait.NewHostPortStrategy(nat.Port(fmt.Sprintf("%d/tcp", internalPort))).
		WithPollInterval(100 * time.Millisecond).
		WithStartupTimeout(5 * time.Minute)

	req := testcontainers.ContainerRequest{

		Image: "docker.elastic.co/elasticsearch/elasticsearch:7.13.1",
		Name:  "elastic-mock" + uuid.NewString(),
		Env: map[string]string{
			"discovery.type": "single-node",
			"ES_JAVA_OPTS":   "-Xms512m -Xmx512m",
		},
		ExposedPorts: []string{fmt.Sprintf("%d:%d/tcp", exposedPort, internalPort)},
		//WaitingFor:   wait.ForLog("started"),
		WaitingFor: ws,
		AutoRemove: true,
	}
	elastic, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return elasticsearch.Config{}, nil, err
	}

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
