package userclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/userclient"
)

const (
	userID      = "2b3a7dac-16f1-4a96-90bc-da3fac9ede81"
	otherUserID = "6f1c1d2e-6a1f-4a5b-8c3d-7e9f0a1b2c3d"
	thirdUserID = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
)

// -----------------------------------------------------------------------------
// test logger
// -----------------------------------------------------------------------------

// testLogger записывает warning-и, чтобы тест мог утверждать "деградация залогирована".
type testLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *testLogger) warn(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}

func (l *testLogger) warnings() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warns...)
}

func (l *testLogger) Debug(string, ...logger.Field) {}
func (l *testLogger) Info(string, ...logger.Field)  {}
func (l *testLogger) Warn(msg string, _ ...logger.Field) {
	l.warn(msg)
}
func (l *testLogger) Error(string, ...logger.Field) {}
func (l *testLogger) Fatal(string, ...logger.Field) {}

func (l *testLogger) DebugWithCtx(context.Context, string, ...logger.Field) {}
func (l *testLogger) InfoWithCtx(context.Context, string, ...logger.Field)  {}
func (l *testLogger) WarnWithCtx(_ context.Context, msg string, _ ...logger.Field) {
	l.warn(msg)
}
func (l *testLogger) ErrorWithCtx(context.Context, string, ...logger.Field) {}
func (l *testLogger) FatalWithCtx(context.Context, string, ...logger.Field) {}

func (l *testLogger) ErrorWithStack(string, ...logger.Field)                        {}
func (l *testLogger) ErrorWithCtxAndStack(context.Context, string, ...logger.Field) {}

var _ logger.Logger = (*testLogger)(nil)

// -----------------------------------------------------------------------------
// fake redis
// -----------------------------------------------------------------------------

// fakeRedis реализует redis.UniversalClient за счёт встраивания интерфейса:
// вызов метода, который тест не переопределил, паникует, а любая запись
// (Set/MSet/SetNX/...) валит тест - инвариант "пакет не пишет в кеш" проверяется здесь.
type fakeRedis struct {
	redis.UniversalClient

	t *testing.T

	mu       sync.Mutex
	getKeys  []string
	mgetKeys [][]string

	getCalls  atomic.Int32
	mgetCalls atomic.Int32

	// values - содержимое "кеша" по ключу; отсутствие ключа даёт промах.
	values map[string]string

	// getErr / mgetErr - если задано, возвращается вместо значения.
	getErr  error
	mgetErr error
}

func newFakeRedis(t *testing.T) *fakeRedis {
	t.Helper()
	return &fakeRedis{t: t, values: map[string]string{}}
}

func (f *fakeRedis) Get(_ context.Context, key string) *redis.StringCmd {
	f.getCalls.Add(1)

	f.mu.Lock()
	f.getKeys = append(f.getKeys, key)
	f.mu.Unlock()

	if f.getErr != nil {
		return redis.NewStringResult("", f.getErr)
	}

	val, ok := f.values[key]
	if !ok {
		return redis.NewStringResult("", redis.Nil)
	}

	return redis.NewStringResult(val, nil)
}

func (f *fakeRedis) MGet(_ context.Context, keys ...string) *redis.SliceCmd {
	f.mgetCalls.Add(1)

	f.mu.Lock()
	f.mgetKeys = append(f.mgetKeys, append([]string(nil), keys...))
	f.mu.Unlock()

	if f.mgetErr != nil {
		return redis.NewSliceResult(nil, f.mgetErr)
	}

	out := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		if val, ok := f.values[key]; ok {
			out = append(out, val)
			continue
		}
		out = append(out, nil)
	}

	return redis.NewSliceResult(out, nil)
}

func (f *fakeRedis) Set(context.Context, string, interface{}, time.Duration) *redis.StatusCmd {
	f.t.Fatal("userclient must never write to redis: Set called")
	return redis.NewStatusResult("", nil)
}

func (f *fakeRedis) SetNX(context.Context, string, interface{}, time.Duration) *redis.BoolCmd {
	f.t.Fatal("userclient must never write to redis: SetNX called")
	return redis.NewBoolResult(false, nil)
}

func (f *fakeRedis) SetEx(context.Context, string, interface{}, time.Duration) *redis.StatusCmd {
	f.t.Fatal("userclient must never write to redis: SetEx called")
	return redis.NewStatusResult("", nil)
}

func (f *fakeRedis) MSet(context.Context, ...interface{}) *redis.StatusCmd {
	f.t.Fatal("userclient must never write to redis: MSet called")
	return redis.NewStatusResult("", nil)
}

func (f *fakeRedis) Del(context.Context, ...string) *redis.IntCmd {
	f.t.Fatal("userclient must never write to redis: Del called")
	return redis.NewIntResult(0, nil)
}

func (f *fakeRedis) observedGetKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.getKeys...)
}

func (f *fakeRedis) observedMGetKeys() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.mgetKeys...)
}

var _ redis.UniversalClient = (*fakeRedis)(nil)

// -----------------------------------------------------------------------------
// fake user service
// -----------------------------------------------------------------------------

// fakeUserService считает запросы, чтобы тест мог доказать "HTTP не дёргался".
type fakeUserService struct {
	*httptest.Server

	calls atomic.Int32

	mu       sync.Mutex
	gotPaths []string
	gotComp  []string
}

// newFakeUserService отдаёт пользователя по id из users, 404 на остальных.
func newFakeUserService(t *testing.T, users map[string]userclient.User) *fakeUserService {
	t.Helper()

	svc := &fakeUserService{}
	svc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc.calls.Add(1)

		svc.mu.Lock()
		svc.gotPaths = append(svc.gotPaths, r.URL.Path)
		svc.gotComp = append(svc.gotComp, r.Header.Get("company_id"))
		svc.mu.Unlock()

		id := r.URL.Path[len("/v1/user/"):]

		user, ok := users[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND"}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(user)
	}))

	t.Cleanup(svc.Close)

	return svc
}

func (s *fakeUserService) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.gotPaths...)
}

func (s *fakeUserService) companyHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.gotComp...)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func cachedUser(t *testing.T, id, language string) string {
	t.Helper()

	raw, err := json.Marshal(userclient.User{ID: id, Language: language})
	require.NoError(t, err)

	return string(raw)
}

func newClient(t *testing.T, cfg userclient.Config) userclient.Client {
	t.Helper()

	if cfg.Logger == nil {
		cfg.Logger = &testLogger{}
	}

	cl, err := userclient.New(cfg)
	require.NoError(t, err)
	require.NotNil(t, cl)

	return cl
}

// -----------------------------------------------------------------------------
// New
// -----------------------------------------------------------------------------

func TestNew_RequiresUserServiceURL(t *testing.T) {
	cl, err := userclient.New(userclient.Config{Logger: &testLogger{}})

	require.ErrorIs(t, err, userclient.ErrEmptyUserServiceURL)
	require.Nil(t, cl)
}

func TestNew_RequiresNonBlankUserServiceURL(t *testing.T) {
	cl, err := userclient.New(userclient.Config{UserServiceURL: "   ", Logger: &testLogger{}})

	require.ErrorIs(t, err, userclient.ErrEmptyUserServiceURL)
	require.Nil(t, cl)
}

func TestNew_RequiresLogger(t *testing.T) {
	cl, err := userclient.New(userclient.Config{UserServiceURL: "http://user-service:8080"})

	require.ErrorIs(t, err, userclient.ErrNilLogger)
	require.Nil(t, cl)
}

func TestNew_NilRedisWarnsButReturnsClient(t *testing.T) {
	log := &testLogger{}

	cl, err := userclient.New(userclient.Config{
		UserServiceURL: "http://user-service:8080",
		Logger:         log,
	})

	require.NoError(t, err)
	require.NotNil(t, cl)
	require.Len(t, log.warnings(), 1)
	require.Contains(t, log.warnings()[0], "Redis client is nil")
}

func TestNew_WithRedisDoesNotWarn(t *testing.T) {
	log := &testLogger{}

	_, err := userclient.New(userclient.Config{
		UserServiceURL: "http://user-service:8080",
		Redis:          newFakeRedis(t),
		Logger:         log,
	})

	require.NoError(t, err)
	require.Empty(t, log.warnings())
}

func TestNew_ImplementsClientInterface(t *testing.T) {
	cl := newClient(t, userclient.Config{UserServiceURL: "http://user-service:8080"})

	require.Implements(t, (*userclient.Client)(nil), cl)
}

// -----------------------------------------------------------------------------
// cache key parity
// -----------------------------------------------------------------------------

// billz_user_service/config.GetUserCacheKey: fmt.Sprintf("user:%s", userID).
// Разойтись с ним нельзя: свой ключ user_service не инвалидирует при обновлении.
func userServiceCacheKey(id string) string {
	return fmt.Sprintf("user:%s", id)
}

func TestCacheKey_DefaultsToUserServiceFormat(t *testing.T) {
	rdb := newFakeRedis(t)
	rdb.values[userServiceCacheKey(userID)] = cachedUser(t, userID, "uz")

	cl := newClient(t, userclient.Config{
		UserServiceURL: "http://user-service:8080",
		Redis:          rdb,
	})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err)
	require.Equal(t, "uz", user.Language)
	require.Equal(t, []string{userServiceCacheKey(userID)}, rdb.observedGetKeys())
	require.Equal(t, userclient.DefaultCacheKeyPrefix+userID, userServiceCacheKey(userID))
}

func TestCacheKey_HonoursCustomPrefix(t *testing.T) {
	rdb := newFakeRedis(t)
	rdb.values["billz-user:"+userID] = cachedUser(t, userID, "ru")

	svc := newFakeUserService(t, nil)

	cl := newClient(t, userclient.Config{
		UserServiceURL: svc.URL,
		Redis:          rdb,
		CacheKeyPrefix: "billz-user:",
	})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err)
	require.Equal(t, userID, user.ID)
	require.Equal(t, []string{"billz-user:" + userID}, rdb.observedGetKeys())
	require.Zero(t, svc.calls.Load())
}
