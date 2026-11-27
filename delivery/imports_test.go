package delivery_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const module = "github.com/billz-2/packages/delivery"

func TestPackagesImportOnlyTheirAllowedDependencies(t *testing.T) {
	out, err := exec.Command("go", "list", "-f",
		`{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}} {{join .XTestImports " "}}`, module+"/...",
	).Output()
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.GreaterOrEqual(t, len(lines), 5, "go list found the packages")
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 3)
		require.Len(t, parts, 3, line)
		pkg := parts[0]
		for _, imp := range strings.Fields(parts[1]) {
			assert.Truef(t, allowed(pkg, imp, false), "%s imports %s", pkg, imp)
		}
		for _, imp := range strings.Fields(parts[2]) {
			assert.Truef(t, allowed(pkg, imp, true), "%s (test) imports %s", pkg, imp)
		}
	}
}

// allowed: the standard library, this module and sarama; cloudevents only in outbox/ce, prometheus only in
// the *prom packages; in tests also testify, lib/pq, sqlx and uuid.
func allowed(pkg, imp string, test bool) bool {
	if !strings.Contains(strings.Split(imp, "/")[0], ".") || within(imp, module) {
		return true
	}

	switch {
	case within(imp, "github.com/IBM/sarama"):
		return true
	case within(imp, "github.com/cloudevents/sdk-go"):
		return within(pkg, module+"/outbox/ce")
	case within(imp, "github.com/prometheus/client_golang"):
		return strings.HasSuffix(strings.TrimSuffix(pkg, "_test"), "prom")
	}

	if test {
		for _, lib := range []string{
			"github.com/stretchr/testify", "github.com/lib/pq", "github.com/jmoiron/sqlx", "github.com/google/uuid",
		} {
			if within(imp, lib) {
				return true
			}
		}
	}

	return false
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

func TestAllowed(t *testing.T) {
	outbox := module + "/outbox"
	assert.True(t, allowed(outbox, "context", false))
	assert.True(t, allowed(outbox, "github.com/IBM/sarama", false))
	assert.True(t, allowed(outbox+"/ce", outbox, false))
	assert.True(t, allowed(outbox+"/ce", "github.com/cloudevents/sdk-go/v2", false))
	assert.True(t, allowed(outbox+"/outboxprom", "github.com/prometheus/client_golang/prometheus", false))
	assert.True(t, allowed(outbox, "github.com/lib/pq", true))

	assert.False(t, allowed(outbox, "github.com/billz-2/packages/pkg/logger", false), "nor the root module")
	assert.False(t, allowed(outbox, "github.com/billz-2/packages/deliveryx", false), "a prefix of a name")
	assert.False(t, allowed(outbox, "github.com/lib/pq", false))
	assert.False(t, allowed(outbox, "github.com/jmoiron/sqlx", false))
	assert.False(t, allowed(outbox, "github.com/cloudevents/sdk-go/v2", false))
	assert.False(t, allowed(outbox, "github.com/prometheus/client_golang/prometheus", false))

	inbox := module + "/inbox"
	assert.True(t, allowed(inbox, "github.com/IBM/sarama", false))
	assert.True(t, allowed(inbox+"/inboxprom", "github.com/prometheus/client_golang/prometheus", false))
	assert.False(t, allowed(inbox, "github.com/prometheus/client_golang/prometheus", false))
	assert.False(t, allowed(inbox, "github.com/cloudevents/sdk-go/v2", false))
	assert.False(t, allowed(inbox, "github.com/lib/pq", false))
}
