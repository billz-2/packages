# userclient

Общий клиент для получения пользователя по id из `billz_user_service`:
сначала Redis, при промахе — `GET /v1/user/{id}`.

## Два инварианта, которые нельзя ломать

1. **Пакет никогда не пишет в Redis.** Кеш прогревает сам `billz_user_service`,
   когда обслуживает `GET /v1/user/:id` (`api/v1/user.go:89`). Второй писатель
   рискует разойтись с ним в формате сериализации — без всякой выгоды.
   Внутри клиент держит Redis как узкий интерфейс только на чтение (`Get` + `MGet`),
   так что записи нет даже технически.
2. **Ключ кеша обязан совпадать с `billz_user_service/config.GetUserCacheKey`** —
   `user:<uuid>`. Свой ключ, свой TTL или собственный кеш в процессе потребителя
   `user_service` не инвалидирует при изменении пользователя (он чистит именно этот
   ключ в `app/service/user.go:430,498`, `events/user_service/user/listener.go:246`,
   `app/service/company.go:307`, `app/service/shop.go:172,227`) — и потребитель будет
   отдавать устаревшие данные, например старый язык.

Отсюда же следует, что Redis — оптимизация, а не зависимость: `nil`-клиент или
ошибка Redis деградируют в HTTP-запрос с warning в лог, но не в ошибку вызова.

## Использование

```go
import (
    "github.com/redis/go-redis/v9"

    "github.com/billz-2/packages/pkg/logger"
    "github.com/billz-2/packages/pkg/userclient"
)

log := logger.New(logger.LevelInfo, "notification-service")

rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisHost + ":" + cfg.RedisPort})

client, err := userclient.New(userclient.Config{
    UserServiceURL: cfg.UserServiceURL, // напр. "http://user-service:8080"
    Redis:          rdb,                // тот же инстанс, в который пишет billz_user_service
    Logger:         log,
})
if err != nil {
    log.Fatal("cannot create user client", logger.Error(err))
}

user, err := client.GetUserByID(ctx, userID)
switch {
case errors.Is(err, userclient.ErrUserNotFound):
    // пользователя нет — берём язык по умолчанию
case err != nil:
    // User Service недоступен — тоже берём язык по умолчанию, уведомление не роняем
default:
    language := user.Language
}
```

Батч — один `MGET` на все id и параллельный дозапрос только промахов:

```go
users, err := client.GetUsersByIDs(ctx, recipientIDs)
// отсутствующих id в map просто нет; err — только если не получили вообще никого
```

Частичный сбой — часть получателей отдалась, часть упала на 5xx или таймауте —
это **успех** с неполным map и `err == nil`: остальные получатели не должны
страдать из-за одного. Единственный след такого сбоя — warning
`partial batch failure` с числами requested/resolved/failed и первой ошибкой.
Кому нужно отличать «пользователя нет» от «не смогли спросить» — вызывайте
`GetUserByID` поштучно.

## Конфигурация

| Поле | По умолчанию | Смысл |
|---|---|---|
| `UserServiceURL` | — (обязательно) | базовый URL `billz_user_service` |
| `Redis` | `nil` | клиент того Redis, в который пишет `billz_user_service`; `nil` = постоянный промах кеша + warning на старте |
| `CacheKeyPrefix` | `user:` | обязан совпадать с `config.GetUserCacheKey` |
| `HTTPTimeout` | `5s` | таймаут запроса к User Service |
| `Logger` | — (обязательно) | `packages/pkg/logger` |

`New` возвращает ошибку при пустом `UserServiceURL` или `nil` `Logger`: неверно
сконфигурированный клиент обязан падать на старте, а не на первой отправке.
`nil` `Redis` — только warning: поиск языка не должен мешать сервису подняться.

## Поведение

| Ситуация | Результат |
|---|---|
| Ключ есть в Redis | пользователь из кеша, HTTP не дёргается |
| `redis.Nil` | HTTP-запрос |
| Ошибка Redis / битый JSON в кеше | warning + HTTP-запрос |
| HTTP 200 | пользователь; кеш прогревает сам `user_service` |
| HTTP 404 | `ErrUserNotFound` |
| id не UUID | `ErrUserNotFound` без сетевого вызова |
| HTTP 5xx / таймаут | обёрнутая ошибка, отличимая от `ErrUserNotFound` через `errors.Is` |

Заголовок `company_id` намеренно не отправляется: вызывающий ищет пользователя по
id, которому уже доверяет, а кросс-компанийная проверка здесь превратила бы поиск
языка в авторизационное решение, которое пакет принимать не в состоянии.

## Моки

```bash
go generate ./pkg/userclient/...
```

```go
ctrl := gomock.NewController(t)
client := mock_userclient.NewMockClient(ctrl)
client.EXPECT().GetUserByID(gomock.Any(), userID).Return(&userclient.User{Language: "uz"}, nil)
```
