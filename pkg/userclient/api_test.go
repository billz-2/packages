package userclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/billz-2/packages/pkg/userclient"
)

// -----------------------------------------------------------------------------
// GetUserByID
// -----------------------------------------------------------------------------

func TestGetUserByID_CacheHit_NoHTTPRequest(t *testing.T) {
	rdb := newFakeRedis(t)
	rdb.values[userServiceCacheKey(userID)] = cachedUser(t, userID, "uz")

	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, Language: "ru"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err)
	require.Equal(t, "uz", user.Language, "value must come from cache, not from user service")
	require.Zero(t, svc.calls.Load(), "cache hit must not issue an HTTP request")
	require.EqualValues(t, 1, rdb.getCalls.Load())
}

func TestGetUserByID_CacheMiss_FallsBackToHTTP(t *testing.T) {
	rdb := newFakeRedis(t)

	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, Language: "uz", FirstName: "Ali"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err)
	require.Equal(t, "uz", user.Language)
	require.Equal(t, "Ali", user.FirstName)
	require.EqualValues(t, 1, svc.calls.Load())
	require.Equal(t, []string{"/v1/user/" + userID}, svc.paths())
}

func TestGetUserByID_DoesNotSendCompanyIDHeader(t *testing.T) {
	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, CompanyID: "some-company"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL})

	_, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err)
	require.Equal(t, []string{""}, svc.companyHeaders())
}

func TestGetUserByID_RedisError_DegradesToHTTP(t *testing.T) {
	log := &testLogger{}

	rdb := newFakeRedis(t)
	rdb.getErr = errors.New("connection refused")

	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, Language: "uz"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb, Logger: log})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err, "a cache outage must never surface as a failure")
	require.Equal(t, "uz", user.Language)
	require.EqualValues(t, 1, svc.calls.Load())
	require.Len(t, log.warnings(), 1)
	require.Contains(t, log.warnings()[0], "redis get failed")
}

func TestGetUserByID_CorruptedCacheValue_DegradesToHTTP(t *testing.T) {
	log := &testLogger{}

	rdb := newFakeRedis(t)
	rdb.values[userServiceCacheKey(userID)] = "{not json"

	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, Language: "uz"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb, Logger: log})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err)
	require.Equal(t, "uz", user.Language)
	require.EqualValues(t, 1, svc.calls.Load())
	require.Len(t, log.warnings(), 1)
}

func TestGetUserByID_NilRedis_AlwaysHitsHTTP(t *testing.T) {
	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, Language: "uz"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL})

	for range 2 {
		user, err := cl.GetUserByID(context.Background(), userID)

		require.NoError(t, err)
		require.Equal(t, "uz", user.Language)
	}

	require.EqualValues(t, 2, svc.calls.Load())
}

func TestGetUserByID_NotFound(t *testing.T) {
	svc := newFakeUserService(t, nil)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.ErrorIs(t, err, userclient.ErrUserNotFound)
	require.Nil(t, user)
	require.EqualValues(t, 1, svc.calls.Load())
}

func TestGetUserByID_ServerError_IsNotNotFound(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(svc.Close)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.Error(t, err)
	require.NotErrorIs(t, err, userclient.ErrUserNotFound)
	require.Contains(t, err.Error(), "500")
	require.Nil(t, user)
}

func TestGetUserByID_Timeout_IsNotNotFound(t *testing.T) {
	release := make(chan struct{})

	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() {
		close(release)
		svc.Close()
	})

	cl := newClient(t, userclient.Config{
		UserServiceURL: svc.URL,
		HTTPTimeout:    50 * time.Millisecond,
	})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.Error(t, err)
	require.NotErrorIs(t, err, userclient.ErrUserNotFound)
	require.Nil(t, user)
}

func TestGetUserByID_MalformedID_NoNetworkCall(t *testing.T) {
	rdb := newFakeRedis(t)
	svc := newFakeUserService(t, nil)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	user, err := cl.GetUserByID(context.Background(), "not-a-uuid")

	require.ErrorIs(t, err, userclient.ErrUserNotFound)
	require.Nil(t, user)
	require.Zero(t, svc.calls.Load())
	require.Zero(t, rdb.getCalls.Load())
}

func TestGetUserByID_TrailingSlashInBaseURL(t *testing.T) {
	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL + "/"})

	_, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err)
	require.Equal(t, []string{"/v1/user/" + userID}, svc.paths())
}

func TestGetUserByID_ToleratesUnknownJSONFields(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + userID + `","language":"uz","brand_new_field":{"a":1}}`))
	}))
	t.Cleanup(svc.Close)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err)
	require.Equal(t, "uz", user.Language)
	require.Equal(t, userID, user.ID)
}

func TestGetUserByID_ToleratesUnknownFieldsFromCache(t *testing.T) {
	rdb := newFakeRedis(t)
	rdb.values[userServiceCacheKey(userID)] = `{"id":"` + userID + `","language":"uz","brand_new_field":42}`

	svc := newFakeUserService(t, nil)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	user, err := cl.GetUserByID(context.Background(), userID)

	require.NoError(t, err)
	require.Equal(t, "uz", user.Language)
	require.Zero(t, svc.calls.Load())
}

// -----------------------------------------------------------------------------
// GetUsersByIDs
// -----------------------------------------------------------------------------

func TestGetUsersByIDs_AllHits_OneMGetNoHTTP(t *testing.T) {
	rdb := newFakeRedis(t)
	rdb.values[userServiceCacheKey(userID)] = cachedUser(t, userID, "uz")
	rdb.values[userServiceCacheKey(otherUserID)] = cachedUser(t, otherUserID, "ru")

	svc := newFakeUserService(t, nil)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID})

	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, "uz", users[userID].Language)
	require.Equal(t, "ru", users[otherUserID].Language)
	require.EqualValues(t, 1, rdb.mgetCalls.Load(), "one MGET for N ids")
	require.Zero(t, svc.calls.Load())
}

func TestGetUsersByIDs_AllMisses_HTTPForEach(t *testing.T) {
	rdb := newFakeRedis(t)

	svc := newFakeUserService(t, map[string]userclient.User{
		userID:      {ID: userID, Language: "uz"},
		otherUserID: {ID: otherUserID, Language: "ru"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID})

	require.NoError(t, err)
	require.Len(t, users, 2)
	require.EqualValues(t, 1, rdb.mgetCalls.Load())
	require.EqualValues(t, 2, svc.calls.Load())
}

func TestGetUsersByIDs_PartialHit_HTTPOnlyForMisses(t *testing.T) {
	rdb := newFakeRedis(t)
	rdb.values[userServiceCacheKey(userID)] = cachedUser(t, userID, "uz")

	svc := newFakeUserService(t, map[string]userclient.User{
		otherUserID: {ID: otherUserID, Language: "ru"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID})

	require.NoError(t, err)
	require.Len(t, users, 2)
	require.EqualValues(t, 1, svc.calls.Load())
	require.Equal(t, []string{"/v1/user/" + otherUserID}, svc.paths())
}

func TestGetUsersByIDs_DuplicateIDsRequestedOnce(t *testing.T) {
	rdb := newFakeRedis(t)

	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, Language: "uz"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, userID, userID})

	require.NoError(t, err)
	require.Len(t, users, 1)
	require.EqualValues(t, 1, svc.calls.Load())

	mgets := rdb.observedMGetKeys()
	require.Len(t, mgets, 1)
	require.Equal(t, []string{userServiceCacheKey(userID)}, mgets[0])
}

func TestGetUsersByIDs_MissingUsersAbsentFromMap(t *testing.T) {
	rdb := newFakeRedis(t)

	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, Language: "uz"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID})

	require.NoError(t, err)
	require.Len(t, users, 1)

	_, ok := users[otherUserID]
	require.False(t, ok, "not-found ids must be absent, not mapped to nil")
}

func TestGetUsersByIDs_AllNotFound_NoError(t *testing.T) {
	svc := newFakeUserService(t, nil)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: newFakeRedis(t)})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID})

	require.NoError(t, err)
	require.Empty(t, users)
}

func TestGetUsersByIDs_AllFail_ReturnsError(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(svc.Close)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: newFakeRedis(t)})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID})

	require.Error(t, err)
	require.Nil(t, users)
}

func TestGetUsersByIDs_PartialFailure_ReturnsWhatItGot(t *testing.T) {
	log := &testLogger{}

	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/user/"+userID {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + userID + `","language":"uz"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(svc.Close)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: newFakeRedis(t), Logger: log})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID})

	require.NoError(t, err, "one broken recipient must not sink the whole batch")
	require.Len(t, users, 1)
	require.Equal(t, "uz", users[userID].Language)

	// Частичный сбой отдаётся как успех - значит, единственный его след это лог.
	require.Len(t, log.warnings(), 1)
	require.Contains(t, log.warnings()[0], "partial batch failure")
}

// Частичный сбой из-за 404 логом не считается: пользователя просто нет, это не сбой.
func TestGetUsersByIDs_PartialNotFound_DoesNotWarn(t *testing.T) {
	log := &testLogger{}

	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, Language: "uz"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: newFakeRedis(t), Logger: log})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID})

	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Empty(t, log.warnings())
}

func TestGetUsersByIDs_EmptyInput(t *testing.T) {
	rdb := newFakeRedis(t)
	svc := newFakeUserService(t, nil)

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	users, err := cl.GetUsersByIDs(context.Background(), nil)

	require.NoError(t, err)
	require.Empty(t, users)
	require.Zero(t, rdb.mgetCalls.Load(), "no MGET with zero keys")
	require.Zero(t, svc.calls.Load())
}

func TestGetUsersByIDs_MalformedIDsSkipped(t *testing.T) {
	rdb := newFakeRedis(t)

	svc := newFakeUserService(t, map[string]userclient.User{
		userID: {ID: userID, Language: "uz"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb})

	users, err := cl.GetUsersByIDs(context.Background(), []string{"", "not-a-uuid", userID})

	require.NoError(t, err)
	require.Len(t, users, 1)

	mgets := rdb.observedMGetKeys()
	require.Len(t, mgets, 1)
	require.Equal(t, []string{userServiceCacheKey(userID)}, mgets[0])
}

func TestGetUsersByIDs_RedisError_DegradesToHTTP(t *testing.T) {
	log := &testLogger{}

	rdb := newFakeRedis(t)
	rdb.mgetErr = errors.New("connection refused")

	svc := newFakeUserService(t, map[string]userclient.User{
		userID:      {ID: userID, Language: "uz"},
		otherUserID: {ID: otherUserID, Language: "ru"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL, Redis: rdb, Logger: log})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID})

	require.NoError(t, err)
	require.Len(t, users, 2)
	require.EqualValues(t, 2, svc.calls.Load())
	require.Len(t, log.warnings(), 1)
	require.Contains(t, log.warnings()[0], "redis mget failed")
}

func TestGetUsersByIDs_NilRedis_HTTPForEach(t *testing.T) {
	svc := newFakeUserService(t, map[string]userclient.User{
		userID:      {ID: userID, Language: "uz"},
		otherUserID: {ID: otherUserID, Language: "ru"},
		thirdUserID: {ID: thirdUserID, Language: "en"},
	})

	cl := newClient(t, userclient.Config{UserServiceURL: svc.URL})

	users, err := cl.GetUsersByIDs(context.Background(), []string{userID, otherUserID, thirdUserID})

	require.NoError(t, err)
	require.Len(t, users, 3)
	require.EqualValues(t, 3, svc.calls.Load())

	paths := svc.paths()
	sort.Strings(paths)
	expected := []string{
		"/v1/user/" + otherUserID,
		"/v1/user/" + thirdUserID,
		"/v1/user/" + userID,
	}
	sort.Strings(expected)
	require.Equal(t, expected, paths)
}
