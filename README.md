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
  client, err := minio.New("localhost:9099", &minio.Options{
    Creds:  credentials.NewStaticV4("test", "test", ""),
    Secure: false,
  })

  if err != nil {
    fmt.Println("Error creating MinIO client:", err)
    return
  }

  streamer := &streamxlsx.XlsxStreamer[testRow]{
    Client: client,
    Config: streamxlsx.Config{
      BucketName:    "excel",
      ObjectName:    "book-temp-2-test",
      PresignExpire: time.Hour,
    },
    Logger: logger.New(logger.LevelInfo, "billz_packages"),
  }

  testChan := make(chan testRow, 300)

  var headers [50]string

  for colID := 0; colID < 50; colID++ {
    headers[colID] = fmt.Sprintf("Column %d", colID+1)
  }

  var req testRow
  rows := make([][]interface{}, 0, 300000)
  for rowID := 0; rowID < 300000; rowID++ {
    row := make([]interface{}, 50)
    for colID := 0; colID < 50; colID++ {
      row[colID] = rand.Intn(640000)
    }

    rows = append(rows, row)

    if rowID > 0 && rowID%1000 == 0 {
      if rowID/1000 == 1 {
        req.Rows = rows[0:1000]
        testChan <- req
      } else {
        req.Rows = rows[1000*(rowID/1000-1) : 1000*rowID/1000]
        testChan <- req
      }
    }
  }

  close(testChan)

  rowConverter := func(req testRow) [][]interface{} {
    if len(req.Rows) == 0 {
      return nil
    }

    return req.Rows
  }

  fileName, err := streamer.StreamTempToMinio(context.Background(), testChan, headers[:], rowConverter)
  fmt.Println(fileName)

  return
}
```

# Mem allocation check for 2 streaming ways 300000 rows with 50 cells per row synthetic data:
## StreamTempToMinio - streaming to temp file and then upload to MinIO
```bash
1370.6670608520508 MB_allocated
1830.7966079711914 MB_total_allocated
9 GC_cycles
```
## StreamPipeToMinio - streaming directly to MinIO via io.Pipe
```bash
2052.8324432373047 MB_allocated
2356.0320434570312 MB_total_allocated
8 GC_cycles
```
## Conclusion
Though streaming via pipe should be more memory efficient MiniO makes huge memory buffer around 600 MB when does not 
know the file size, and buffer grows wth every write operation that goes out of memory limits. As we can see memory allocation
differs less than 600 MB. So using temp file is more memory efficient while xlsx does not support proper streaming with
memory flushes and cursor management.


