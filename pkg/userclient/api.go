package userclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/billz-2/packages/pkg/logger"
)

// maxErrorBodySize - сколько байт тела ошибочного ответа попадает в текст ошибки.
const maxErrorBodySize = 512

// GetUserByID возвращает пользователя по id: сначала Redis, при промахе - User Service.
func (c *client) GetUserByID(ctx context.Context, userID string) (*User, error) {
	if !isUUID(userID) {
		// Ручка ответила бы 400; сеть тут дёргать незачем.
		return nil, fmt.Errorf("%w: id %q is not a uuid", ErrUserNotFound, userID)
	}

	if user := c.getFromCache(ctx, userID); user != nil {
		return user, nil
	}

	return c.getFromUserService(ctx, userID)
}

// GetUsersByIDs забирает всё, что есть в кеше, одним MGET и дозапрашивает в
// User Service только промахи.
func (c *client) GetUsersByIDs(ctx context.Context, userIDs []string) (map[string]*User, error) {
	ids := dedupeUUIDs(userIDs)

	result := make(map[string]*User, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	misses := c.getManyFromCache(ctx, ids, result)
	if len(misses) == 0 {
		return result, nil
	}

	fetched, firstErr := c.fetchMany(ctx, misses)
	for id, user := range fetched {
		result[id] = user
	}

	// Ошибка только если не удалось получить вообще никого: частичный результат
	// полезнее отказа - остальные получатели не должны страдать из-за одного.
	if len(result) == 0 && firstErr != nil {
		return nil, firstErr
	}

	return result, nil
}

// getFromCache возвращает nil на любом промахе или сбое: кеш - оптимизация, не зависимость.
func (c *client) getFromCache(ctx context.Context, userID string) *User {
	if c.cache == nil {
		return nil
	}

	key := c.cacheKey(userID)

	data, err := c.cache.Get(ctx, key).Bytes()
	switch {
	case errors.Is(err, redis.Nil):
		return nil
	case err != nil:
		c.logger.WarnWithCtx(ctx, "userclient: redis get failed, falling back to User Service",
			logger.String("key", key), logger.Error(err))
		return nil
	}

	var user User
	if err = json.Unmarshal(data, &user); err != nil {
		c.logger.WarnWithCtx(ctx, "userclient: cannot unmarshal cached user, falling back to User Service",
			logger.String("key", key), logger.Error(err))
		return nil
	}

	return &user
}

// getManyFromCache складывает найденных в dst и возвращает id, которых в кеше нет.
func (c *client) getManyFromCache(ctx context.Context, ids []string, dst map[string]*User) []string {
	if c.cache == nil {
		return ids
	}

	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, c.cacheKey(id))
	}

	values, err := c.cache.MGet(ctx, keys...).Result()
	if err != nil {
		c.logger.WarnWithCtx(ctx, "userclient: redis mget failed, falling back to User Service",
			logger.Int("keys", len(keys)), logger.Error(err))
		return ids
	}

	misses := make([]string, 0, len(ids))
	for i, id := range ids {
		if i >= len(values) {
			misses = append(misses, id)
			continue
		}

		raw, ok := values[i].(string)
		if !ok { // nil - ключа нет
			misses = append(misses, id)
			continue
		}

		var user User
		if err = json.Unmarshal([]byte(raw), &user); err != nil {
			c.logger.WarnWithCtx(ctx, "userclient: cannot unmarshal cached user, falling back to User Service",
				logger.String("key", keys[i]), logger.Error(err))
			misses = append(misses, id)
			continue
		}

		dst[id] = &user
	}

	return misses
}

// fetchMany дозапрашивает промахи в User Service с ограниченным параллелизмом.
// ErrUserNotFound ошибкой не считается - такого пользователя просто нет в результате.
func (c *client) fetchMany(ctx context.Context, ids []string) (map[string]*User, error) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		fetched  = make(map[string]*User, len(ids))
		firstErr error
	)

	workers := c.concurrency
	if workers <= 0 || workers > len(ids) {
		workers = len(ids)
	}

	sem := make(chan struct{}, workers)

	for _, id := range ids {
		wg.Add(1)

		go func(id string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			user, err := c.getFromUserService(ctx, id)

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				fetched[id] = user
			case errors.Is(err, ErrUserNotFound):
			case firstErr == nil:
				firstErr = err
			}
		}(id)
	}

	wg.Wait()

	return fetched, firstErr
}

// getFromUserService - GET {baseURL}/v1/user/{id}. Ответ - неконвертированный models.User,
// без обёртки. Заголовок company_id намеренно не отправляется: вызывающий ищет
// пользователя по id, которому уже доверяет, а кросс-компанийная проверка здесь
// превратила бы поиск языка в авторизационное решение.
func (c *client) getFromUserService(ctx context.Context, userID string) (*User, error) {
	url := fmt.Sprintf("%s/v1/user/%s", c.baseURL, userID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("userclient: build request for user %s: %w", userID, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userclient: request user %s: %w", userID, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: id %s", ErrUserNotFound, userID)
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		return nil, fmt.Errorf("userclient: user service returned %d for user %s: %s",
			resp.StatusCode, userID, string(body))
	}

	var user User
	if err = json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("userclient: decode user %s: %w", userID, err)
	}

	return &user, nil
}

func isUUID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

// dedupeUUIDs сохраняет порядок, выбрасывает дубли и не-UUID: для них ручка
// всё равно ответила бы 400, а в результат они попасть не могут.
func dedupeUUIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))

	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}

		if !isUUID(id) {
			continue
		}

		out = append(out, id)
	}

	return out
}
