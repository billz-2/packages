package test_environment

import (
	"context"
	"fmt"
	"time"

	dockerNetwork "github.com/docker/docker/api/types/network"
	"github.com/pkg/errors"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
)

func CreateDockerNetwork(ctx context.Context) (*testcontainers.DockerNetwork, error) {
	networkName := fmt.Sprintf("test-network-%d", time.Now().UnixNano())
	ipamConfig := dockerNetwork.IPAM{
		Driver: "default",
		Config: []dockerNetwork.IPAMConfig{
			{
				Subnet:  "10.1.1.0/24",
				Gateway: "10.1.1.254",
			},
		},
		Options: map[string]string{
			"driver": "host-local",
		},
	}
	net, err := network.New(ctx,
		network.WithIPAM(&ipamConfig),
		network.WithAttachable(),
		network.WithDriver("bridge"),
		network.WithLabels(map[string]string{"name": networkName}),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create network")
	}

	return net, nil
}
