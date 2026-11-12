package test_environment

import (
	"strings"
	"testing"
)

func TestElasticsearchDockerfileBaseImage(t *testing.T) {
	t.Setenv(EnvElasticsearchBaseImage, "")
	if got := elasticsearchDockerfile(); !strings.HasPrefix(got, "FROM "+defaultElasticsearchImage+"\n") {
		t.Fatalf("default base: got %q", got)
	}

	t.Setenv(EnvElasticsearchBaseImage, "elastic/elasticsearch:9.0.4")
	got := elasticsearchDockerfile()
	if !strings.HasPrefix(got, "FROM elastic/elasticsearch:9.0.4\n") {
		t.Fatalf("override base: got %q", got)
	}
	if !strings.Contains(got, "elasticsearch-plugin install --batch analysis-icu") {
		t.Fatalf("override base lost the plugin: got %q", got)
	}
}
