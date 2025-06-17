package test

import (
	"github.com/billz-2/packages/pkg/test_environment"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetFreePort(t *testing.T) {
	n := 10
	for i := 0; i < n; i++ {
		port, err := test_environment.GetFreePort()
		require.NoError(t, err)
		require.NotEqual(t, 0, port)
	}
}
