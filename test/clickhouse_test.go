package test

import (
	"context"
	"testing"

	"github.com/billz-2/packages/pkg/test_environment"
	"github.com/stretchr/testify/require"
)

func TestClickHouseContainer(t *testing.T) {
	config := test_environment.Config{
		ClickHouseUser:     "test_user",
		ClickHousePassword: "test_password",
		ClickHouseDatabase: "test_db",
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Initialize the ClickHouse container
	container, err := test_environment.SetupClickhouse(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create ClickHouse container: %v", err)
	}

	require.NoError(t, err)
	err = container.Close(ctx)
	cancel()
	require.NoError(t, err)
}
