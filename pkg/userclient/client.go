package userclient

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/billz-2/packages/pkg/logger"
)

const (
	// DefaultCacheKeyPrefix должен совпадать с billz_user_service/config.GetUserCacheKey ("user:%s").
	DefaultCacheKeyPrefix = "user:"

	// DefaultHTTPTimeout - таймаут запроса к User Service по умолчанию.
	DefaultHTTPTimeout = 5 * time.Second

	// defaultBatchConcurrency - сколько промахов кеша дозапрашиваются в User Service одновременно.
	defaultBatchConcurrency = 8
)

// Config - конфигурация процесса, а не отдельного вызова: базовый URL и Redis
// одинаковы для всех обращений, а http.Client с таймаутом должен кому-то принадлежать.
type Config struct {
	// UserServiceURL - базовый URL billz_user_service, напр. "http://user-service:8080".
	UserServiceURL string

	// Redis - клиент того инстанса Redis, в который пишет billz_user_service.
	// nil допустим и означает "кеша нет": каждый вызов идёт в User Service.
	// New в этом случае пишет warning - это деградация, а не штатный режим.
	Redis redis.UniversalClient

	// CacheKeyPrefix - префикс ключа. По умолчанию "user:", должен совпадать с
	// billz_user_service config.GetUserCacheKey.
	CacheKeyPrefix string

	// HTTPTimeout - таймаут запроса к User Service. По умолчанию 5s.
	HTTPTimeout time.Duration

	Logger logger.Logger
}

// Client - единственный способ получить пользователя по id: Redis, при промахе - User Service.
type Client interface {
	// GetUserByID возвращает пользователя по id: сначала Redis, при промахе - User Service.
	// Возвращает ErrUserNotFound, если User Service ответил 404 или id не UUID.
	GetUserByID(ctx context.Context, userID string) (*User, error)

	// GetUsersByIDs возвращает пользователей по набору id одним MGET и параллельными
	// запросами в User Service только для промахов. Отсутствующие пользователи в
	// результат не попадают; ошибка возвращается только если не удалось получить ни одного.
	GetUsersByIDs(ctx context.Context, userIDs []string) (map[string]*User, error)
}

// cacheReader - намеренно узкий взгляд на redis.UniversalClient: только чтение.
// Инвариант "пакет никогда не пишет в кеш" держится типом, а не дисциплиной.
type cacheReader interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	MGet(ctx context.Context, keys ...string) *redis.SliceCmd
}

// Verify interface compliance.
var _ Client = (*client)(nil)

type client struct {
	baseURL     string
	cache       cacheReader
	keyPrefix   string
	concurrency int

	http   *http.Client
	logger logger.Logger
}

// New проверяет конфигурацию и возвращает клиент.
//
// Отсутствие UserServiceURL или Logger - ошибка: неверно сконфигурированный клиент
// обязан падать на старте, а не на первой отправке уведомления. Отсутствие Redis -
// только warning: сервисы уже переживают недоступность Redis на старте, и поиск
// языка не должен быть тем, что мешает сервису подняться.
func New(cfg Config) (Client, error) {
	if strings.TrimSpace(cfg.UserServiceURL) == "" {
		return nil, ErrEmptyUserServiceURL
	}
	if cfg.Logger == nil {
		return nil, ErrNilLogger
	}

	keyPrefix := cfg.CacheKeyPrefix
	if keyPrefix == "" {
		keyPrefix = DefaultCacheKeyPrefix
	}

	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = DefaultHTTPTimeout
	}

	var cache cacheReader
	if cfg.Redis != nil {
		cache = cfg.Redis
	} else {
		cfg.Logger.Warn(
			"userclient: Redis client is nil, every lookup will hit User Service",
			logger.String("user_service_url", cfg.UserServiceURL),
		)
	}

	return &client{
		baseURL:     strings.TrimRight(strings.TrimSpace(cfg.UserServiceURL), "/"),
		cache:       cache,
		keyPrefix:   keyPrefix,
		concurrency: defaultBatchConcurrency,
		http:        &http.Client{Timeout: timeout},
		logger:      cfg.Logger,
	}, nil
}

// cacheKey строит ключ ровно так же, как billz_user_service/config.GetUserCacheKey.
func (c *client) cacheKey(userID string) string {
	return c.keyPrefix + userID
}
