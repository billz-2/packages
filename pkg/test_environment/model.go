package test_environment

type Config struct {
	Environment           string
	PostgresHost          string
	PostgresPort          int
	PostgresUser          string
	PostgresPassword      string
	PostgresDatabase      string
	ElasticSearchUrls     []string
	ElasticSearchUser     string
	ElasticSearchPassword string
	RedisAddress          string
	RedisPort             string
	RedisPassword         string
	ClickHouseUser        string
	ClickHousePassword    string
	ClickHouseDatabase    string
}
