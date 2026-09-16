package test_environment

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
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

// defaultElasticsearchImage is the base image the package builds on when
// EnvElasticsearchImage is not set. Keep it in sync with the Elasticsearch
// version the package has historically targeted, so existing callers don't
// change behavior.
const defaultElasticsearchImage = "docker.elastic.co/elasticsearch/elasticsearch:9.0.4"

// elasticsearchDockerfile is the build recipe used when no override image is
// supplied. It bakes the analysis-icu plugin into the default image so it is
// available without any runtime installation.
const elasticsearchDockerfile = "FROM " + defaultElasticsearchImage + "\n" +
	"RUN CLI_JAVA_OPTS=\"-Xms256m -Xmx256m\" elasticsearch-plugin install --batch analysis-icu\n"

// elasticsearchBuildContext returns a tar archive containing the inline
// Dockerfile above, suitable for testcontainers' FromDockerfile.ContextArchive.
// Building the context in-memory keeps pkg/test_environment self-contained so
// importing services don't need to vendor a Dockerfile on disk.
func elasticsearchBuildContext() io.ReadSeeker {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	content := []byte(elasticsearchDockerfile)
	// The error is intentionally ignored: writing a fixed, small payload to an
	// in-memory buffer cannot fail in practice.
	_ = tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Mode: 0o600,
		Size: int64(len(content)),
	})
	_, _ = tw.Write(content)
	_ = tw.Close()

	return bytes.NewReader(buf.Bytes())
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

	// The stock Elasticsearch image does not bundle the analysis-icu
	// plugin, which some services rely on. Instead of installing it at
	// runtime and restarting the container (slow, requires network access
	// on every run, and races the wait strategy), we bake the plugin into
	// the image at build time via a tiny inline Dockerfile. Docker caches
	// the resulting layer, so subsequent runs are fast and work offline.
	//
	// If a caller overrides the image via EnvElasticsearchImage, we assume
	// it already ships the plugins they need and use it directly.
	if img := os.Getenv(EnvElasticsearchImage); img != "" {
		req.Image = img
	} else {
		req.FromDockerfile = testcontainers.FromDockerfile{
			ContextArchive: elasticsearchBuildContext(),
			// keep the built image so it is reused across runs
			Repo:      "billz-test/elasticsearch-icu",
			Tag:       "9.0.4",
			KeepImage: true,
		}
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
