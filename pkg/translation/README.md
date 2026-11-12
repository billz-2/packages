# translation

Общий код переводов текстов из БД сервиса на языки `ru|en|uz`.

## Состав

- `Lang`, `NormalizeLang` — язык и приведение к whitelist (иначе `ru`).
- `Dict`, `Dict.Get(key, fallback)` — плоский словарь; nil-safe; нет ключа или пустое значение дают `fallback`.
- `LoadDict` — загрузка колонки языка своей строки из таблицы `translations`.

## Формат ключей

Плоский, внутри каждой языковой колонки: `<сущность>.<id>.<поле>`, UUID в нижнем регистре
(`permission.<uuid>.name`, `permission.<uuid>.description`, `default_role.<русское имя роли>`).

## Инварианты

- `ru` не загружается: `LoadDict` для `LangRu` возвращает `nil, nil` без запроса, при выдаче берётся исходное значение из БД.
- Колонка выбирается только из whitelist (`switch lang` по двум константным запросам), имя колонки из ввода не подставляется.
- Пакет не кеширует словарь в памяти: один запрос на ответ.

## Консьюмер

`NewConsumerHandler(ConsumerConfig{DB, ServiceName, Logger})` создаёт хендлер, который сохраняет словари других сервисов
из топика `Topic(<сервис>)` в таблицу `translations`.

- Своё сообщение (`service_name` равен имени сервиса) пропускается тихо, без записи в лог.
- Пропускаются с предупреждением в лог (`nil`): пустой `service_name`, `source` события не равен `service_name`,
  `version <= 0`, `ru/en/uz` не JSON-объекты со строковыми значениями, битое тело. `Retry` на битом сообщении остановил бы всю consumer-группу.
- Запись — один upsert с условием `translations.version < EXCLUDED.version`: старые и повторные версии игнорируются атомарно.
  Чужая строка всегда пишется с `saved = true`.
- Ошибка БД возвращается как `fmt.Errorf("%w: %w", ErrRetry, err)`.

Пример адаптера к `models.Response` сервиса:

```go
func (c *Consumer) HandleTranslations(ctx context.Context, e cloudevents.Event) models.Response {
	if err := c.translations.Handle(ctx, e); err != nil {
		return models.Response{Retry: errors.Is(err, translation.ErrRetry)}
	}

	return models.Response{}
}
```

## Демон публикации

`NewDaemon(DaemonConfig{DB, Publisher, ServiceName, PollInterval, HeartbeatInterval, Logger})` создаёт демон, который
публикует собственную строку `translations` сервиса в топик `Topic(<сервис>)` (`v1.<сервис>.translations.updated`).

- `Run(ctx)` блокирующий: сразу делает heartbeat, затем по тикерам опрашивает несохранённую строку и шлёт heartbeat до отмены `ctx`.
- `SyncOnce(ctx, force)` — один проход: публикует строку, если `saved = false` (или `force = true`), и возвращает, была ли публикация.
  Параллельные демоны разных реплик разводятся через `pg_try_advisory_xact_lock`: кто не получил блокировку, молча пропускает проход.
- Интервалы по умолчанию (нулевые значения заменяются): опрос `DefaultPollInterval` = 1 ч, heartbeat `DefaultHeartbeatInterval` = 24 ч.
- `Publisher` — интерфейс `PushWithKey(topic string, e cloudevents.Event, key string) error`; ключ сообщения — `service_name`.
- `Message` — тело события (JSON): `service_name`, `version` (int64), `ru`, `en`, `uz` (объекты `ключ -> строка`).
  `source` события равен `service_name`, тип события равен `Topic(service_name)`.
- Строка помечается сохранённой только после успешной публикации и только если версия не изменилась за время публикации.

## Требования к схеме

В БД сервиса нужны (строго в таком порядке): таблица `translations` -> функция `translations_mark_unsaved()` ->
триггер `translations_mark_unsaved_trg` -> `INSERT` своей строки.

- Триггер `BEFORE INSERT OR UPDATE ... FOR EACH ROW` вызывает функцию с `TG_ARGV[0]` = имя своего сервиса
  (`EXECUTE FUNCTION translations_mark_unsaved('<сервис>')`). Строки других сервисов триггер не трогает.
- Свою строку вставляют после создания триггера, чтобы она получила `saved = false` и `version = 1` и ушла в Kafka.
- Образец — `testdata/schema.sql`.

## Чек-лист топика

- Включён `cleanup.policy=compact` или `retention.ms=-1`: последняя версия словаря каждого сервиса не должна пропадать.
- `max.message.bytes` не меньше 1 МБ (словарь передаётся целиком одним сообщением).
- Ключ сообщения — `service_name` (compaction хранит последнюю версию по ключу).
- Размер сообщения больше 700 КБ демон логирует предупреждением (`translations message is large`): пора разбираться с размером словаря.
