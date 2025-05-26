# billz_platform

package for common functions for all microservices

- how to add new package version:

  - push changes into master
  - git tag -a v0.0.13 -m "skip stack level"
  - git push origin v0.0.13
  - wait some time (up to 30 minutes) for proxy to update
  - to update package in microservice execute `go get -u github.com/billz-2/packages`

- how to execute tests
  - docker compose -f docker-compose.test.yml up -d
  - go test ./...

# streamxlsx

Пакет для потоковой генерации XLSX-отчётов из данных в Go с записью напрямую в MinIO (S3-совместимый storage).

## Возможности

- Генерация XLSX с минимальным потреблением памяти
- Потоковая запись из канала данных
- Интеграция с MinIO через `PutObject` с потоковым чтением
- Поддержка кастомных функций записи строки в XLSX (для разных типов данных)
- Возвращает публичную ссылку на скачивание файла после загрузки
- Конфигурируемые параметры MinIO и бакета

## Установка

```bash
go get github.com/billz-2/packages/streamxlsx
```

## Пример использования

```go
package main

import (
  "context"
  "fmt"
  "time"

  "github.com/billz-2/packages/pkg/logger"
  "github.com/billz-2/packages/pkg/streamxlsx"
  "github.com/minio/minio-go/v7"
  "github.com/minio/minio-go/v7/pkg/credentials"
)

// UserRecord - пример структуры данных для экспорта
type UserRecord struct {
  ID        int64
  Name      string
  Email     string
  CreatedAt time.Time
  IsActive  bool
  Balance   float64
}

func main() {
  // Инициализация логгера
  log := logger.NewZapLogger()

  // Настройка MinIO клиента
  minioClient, err := minio.New("minio.example.com:9000", &minio.Options{
    Creds:  credentials.NewStaticV4("minioadmin", "minioadmin", ""),
    Secure: true,
  })
  if err != nil {
    log.Fatal("Cannot initialize MinIO client", logger.Error(err))
  }

  // Настройка конфигурации для экспорта
  config := streamxlsx.Config{
    BucketName:    "reports",
    ObjectName:    fmt.Sprintf("users-export-%s.xlsx", time.Now().Format("2006-01-02")),
    PresignExpire: 3600, // URL будет действителен 1 час
  }

  // Создание XlsxStreamer
  streamer := &streamxlsx.XlsxStreamer[UserRecord]{
    Client: minioClient,
    Config: config,
    Logger: log,
  }

  // Создание канала данных
  dataCh := make(chan []UserRecord)

  // Функция для конвертации UserRecord в строку Excel
  rowConverter := func(user UserRecord) []interface{} {
    return []interface{}{
      user.ID,
      user.Name,
      user.Email,
      user.CreatedAt,
      user.IsActive,
      user.Balance,
    }
  }

  // Контекст с таймаутом для всей операции
  ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
  defer cancel()

  // Запускаем асинхронную обработку в отдельной горутине
  go func() {
    defer close(dataCh) // Важно закрыть канал по завершении!

    // Первый батч - заголовки (можно записать отдельно)
    headers := []UserRecord{{
      Name:    "ID",
      Email:   "Имя",
      Balance: 0, // Место для "Email"
      // остальные поля будут пустыми
    }}
    dataCh <- headers

    // Имитация получения данных батчами
    for i := 0; i < 5; i++ {
      // Создаем батч из 1000 записей
      batch := make([]UserRecord, 0, 1000)
      for j := 0; j < 1000; j++ {
        user := UserRecord{
          ID:        int64(i*1000 + j),
          Name:      fmt.Sprintf("User %d", i*1000+j),
          Email:     fmt.Sprintf("user%d@example.com", i*1000+j),
          CreatedAt: time.Now().AddDate(0, 0, -j),
          IsActive:  j%5 != 0, // каждый пятый неактивен
          Balance:   float64(j) * 10.5,
        }
        batch = append(batch, user)
      }

      // Проверка отмены контекста перед отправкой
      if ctx.Err() != nil {
        log.Warn("Context canceled, stopping data generation", logger.Error(ctx.Err()))
        return
      }

      // Отправляем батч в канал
      dataCh <- batch

      // Имитация задержки обработки данных
      time.Sleep(100 * time.Millisecond)
    }

    log.Info("All data batches sent to channel")
  }()

  // Запускаем стриминг данных в MinIO
  downloadURL, err := streamer.StreamToMinio(ctx, "Users", dataCh, rowConverter)
  if err != nil {
    log.Fatal("Failed to stream data to MinIO", logger.Error(err))
  }

  log.Info("Export completed successfully", logger.String("download_url", downloadURL))
}
```