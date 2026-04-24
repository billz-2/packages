package test_environment

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/pkg/errors"
	"github.com/testcontainers/testcontainers-go"
	clickhouseModule "github.com/testcontainers/testcontainers-go/modules/clickhouse"
	testContainersNetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ClickhouseContainer представляет собой контейнер с Clickhouse для тестирования
type ClickhouseContainer struct {
	chContainer testcontainers.Container
	zooKeeper   testcontainers.Container
	TCPPort     nat.Port
	HTTPPort    nat.Port
	Host        string
	Username    string
	Password    string
	Database    string
}

const internalTCPClickhousePort = 9000
const internalHttpClickhousePort = 8123

// SetupClickhouse создает и запускает контейнер Clickhouse для тестов
func SetupClickhouse(ctx context.Context, cfg Config, network *testcontainers.DockerNetwork) (*ClickhouseContainer, error) {
	// Настройки по умолчанию
	exposedTCPPort, err := GetFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to get free Clickhouse TCP port: %w", err)
	}

	exposedHTTPPort, err := GetFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to get free Clickhouse HTTP port: %w", err)
	}

	zkPort := nat.Port("2181/tcp")

	zooKeeperContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			ExposedPorts: []string{zkPort.Port()},
			Image:        "zookeeper:3.8",
			WaitingFor:   wait.ForListeningPort(zkPort),
			Networks:     []string{network.Name},
		},
		Started: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create zooKeeper container: %w", err)
	}

	ipaddr, err := zooKeeperContainer.ContainerIP(ctx)

	cfgFile, err := createClickHouse01Config()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Clickhouse config file")
	}

	defer func() {
		err = os.Remove(cfgFile)
		if err != nil {
			fmt.Printf("failed to remove temporary Clickhouse config file: %v\n", err)
		}
	}()

	clickHouseContainer, err := clickhouseModule.Run(ctx,
		"clickhouse/clickhouse-server:24.2.2-alpine",
		clickhouseModule.WithUsername(cfg.ClickHouseUser),
		clickhouseModule.WithPassword(cfg.ClickHousePassword),
		clickhouseModule.WithDatabase(cfg.ClickHouseDatabase),
		testcontainers.WithExposedPorts(
			fmt.Sprintf("%d:%d/tcp", exposedTCPPort, internalTCPClickhousePort),
			fmt.Sprintf("%d:%d/tcp", exposedHTTPPort, internalHttpClickhousePort),
		),
		clickhouseModule.WithZookeeper(ipaddr, zkPort.Port()),
		clickhouseModule.WithConfigFile(cfgFile),
		testContainersNetwork.WithNetwork([]string{network.Name}, network),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to create Clickhouse container: %w", err)
	}

	maxRetries := 8
	for i := 0; i < maxRetries; i++ {
		_, err = clickHouseContainer.Endpoint(ctx, "")
		if err == nil {
			break
		}

		if i == maxRetries-1 {
			return nil, fmt.Errorf("failed to get endpoint after %d retries: %w", maxRetries, err)
		}

		time.Sleep(time.Second * 10)
	}

	mappedTCPPort, err := clickHouseContainer.MappedPort(ctx, nat.Port(fmt.Sprintf("%d/tcp", internalTCPClickhousePort)))
	if err != nil {
		return nil, fmt.Errorf("failed to get mapped TCP port: %w", err)
	}

	mappedHTTPPort, err := clickHouseContainer.MappedPort(ctx, nat.Port(fmt.Sprintf("%d/tcp", internalHttpClickhousePort)))
	if err != nil {
		return nil, fmt.Errorf("failed to get mapped HTTP port: %w", err)
	}

	host, err := clickHouseContainer.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}

	var clickhouseConn *sql.DB
	var connectErr error

	// Add retry logic for database connection
	maxRetries = 8
	for i := 0; i < maxRetries; i++ {
		clickhouseConn = clickhouse.OpenDB(&clickhouse.Options{
			Addr: []string{fmt.Sprintf("%s:%s", host, mappedTCPPort.Port())},
			Auth: clickhouse.Auth{
				Database: cfg.ClickHouseDatabase,
				Username: cfg.ClickHouseUser,
				Password: cfg.ClickHousePassword,
			},
			Settings: clickhouse.Settings{
				"max_execution_time": 60,
			},
			DialTimeout: 30 * time.Second,
			Compression: &clickhouse.Compression{
				Method: clickhouse.CompressionLZ4,
			},
		})

		connectErr = clickhouseConn.Ping()
		_ = clickhouseConn.Close()
		if connectErr == nil {
			// Успешное подключение
			break
		}

		// Ждем перед следующей попыткой
		if i < maxRetries-1 {
			time.Sleep(time.Second * 5)
		}
	}

	result := ClickhouseContainer{
		chContainer: clickHouseContainer,
		zooKeeper:   zooKeeperContainer,
		TCPPort:     mappedTCPPort,
		HTTPPort:    mappedHTTPPort,
		Host:        host,
		Username:    cfg.ClickHouseUser,
		Password:    cfg.ClickHousePassword,
		Database:    cfg.ClickHouseDatabase,
	}

	if connectErr != nil {
		initErr := fmt.Errorf("could not connect to ClickHouse after %d retries error: %w", maxRetries, connectErr)
		err = result.Stop(ctx) // Clean up resources if connection fails
		if err != nil {
			return nil, errors.Wrap(err, initErr.Error())
		}

		return nil, initErr
	}

	return &result, nil
}

// Stop останавливает и удаляет все контнейры для Clickhouse cluster
func (c *ClickhouseContainer) Stop(ctx context.Context) error {
	if c.chContainer != nil {
		containerState, err := c.chContainer.State(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to get ClickHouse container state")
		}

		if !containerState.Running && containerState.Status != container.StateRunning {
			return nil
		}

		err = c.chContainer.Terminate(ctx, testcontainers.RemoveVolumes())
		if err != nil {
			return errors.Wrap(err, "can't stop clickhouse")
		}

		// Ожидаем полной остановки ClickHouse контейнера
		maxRetries := 10
		for i := 0; i < maxRetries; i++ {
			var state *container.State
			state, err = c.chContainer.State(ctx)
			if err != nil || !state.Running {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	if c.zooKeeper != nil {
		containerState, err := c.zooKeeper.State(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to get ZooKeeper container state")
		}

		if !containerState.Running && containerState.Status != container.StateRunning {
			return nil
		}

		err = c.zooKeeper.Terminate(ctx, testcontainers.RemoveVolumes())
		if err != nil {
			return errors.Wrap(err, "can't stop ZooKeeper")
		}

		// Ожидаем полной остановки ZooKeeper контейнера
		maxRetries := 10
		for i := 0; i < maxRetries; i++ {
			var state *container.State
			state, err = c.zooKeeper.State(ctx)
			if err != nil || !state.Running {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	return nil
}

func createClickHouse01Config() (string, error) {
	cfgXmlString := `
<clickhouse>
	<company>
		<logger>
			<level>debug</level>
			<console>true</console>
			<log remove="remove"/>
			<errorlog remove="remove"/>
		</logger>
	
		<query_log>
			<database>system</database>
			<table>query_log</table>
		</query_log>
	
		<listen_host>0.0.0.0</listen_host>
		<http_port>8123</http_port>
		<tcp_port>9000</tcp_port>
		<interserver_http_host>clickhouse01</interserver_http_host>
		<interserver_http_port>9009</interserver_http_port>
	
		<max_connections>4096</max_connections>
		<keep_alive_timeout>3</keep_alive_timeout>
		<max_concurrent_queries>100</max_concurrent_queries>
		<uncompressed_cache_size>8589934592</uncompressed_cache_size>
		<mark_cache_size>5368709120</mark_cache_size>
	
		<path>/var/lib/clickhouse/</path>
		<tmp_path>/var/lib/clickhouse/tmp/</tmp_path>
		<user_files_path>/var/lib/clickhouse/user_files/</user_files_path>
	
		<users_config>users.xml</users_config>
		<default_profile>default</default_profile>
		<default_database>default</default_database>
		<mlock_executable>false</mlock_executable>
	
		<remote_servers>
			<cluster>
				<shard>
					<replica>
						<host>clickhouse01</host>
						<port>9000</port>
					</replica>
				</shard>
			</cluster>
		</remote_servers>
	
		<zookeeper>
			<node index="1">
				<host>zookeeper</host>
				<port>2181</port>
			</node>
		</zookeeper>
	
		<macros>
			<cluster>cluster</cluster>
			<shard>01</shard>
			<replica>clickhouse01</replica>
		</macros>
	
		<distributed_ddl>
			<path>/clickhouse/task_queue/ddl</path>
		</distributed_ddl>
	
		<format_schema_path>/var/lib/clickhouse/format_schemas/</format_schema_path>
	</company>
</clickhouse>
`
	tempFile, err := os.CreateTemp("", "clickhouse_config_*.xml")
	if err != nil {
		return "", errors.Wrap(err, "failed to create temporary file for Clickhouse config")
	}

	defer func() {
		err = tempFile.Close()
		if err != nil {
			fmt.Printf("failed to close temporary file: %v\n", err)
		}
	}()

	// Записываем конфигурацию во временный файл
	_, err = tempFile.WriteString(cfgXmlString)
	if err != nil {
		return "", errors.Wrap(err, "failed to write configuration to file")
	}

	return tempFile.Name(), nil
}
