package streamxlsx

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"sync"
	"time"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/minio/minio-go/v7"
	"github.com/pkg/errors"
)

// ExcelLibrary определяет, какую библиотеку использовать для работы с Excel

// MinioClient нужен для абстракции клиента MinIO / S3
type MinioClient interface {
	PutObject(ctx context.Context, bucketName, objectName string, reader io.Reader, objectSize int64, opts minio.PutObjectOptions) (minio.UploadInfo, error)
	PresignedGetObject(ctx context.Context, bucketName, objectName string, expirySeconds int64, reqParams map[string]string) (string, error)
}

// Config для стрима
type Config struct {
	BucketName    string
	ObjectName    string
	PresignExpire int64 // в секундах, 0 - не генерировать
}

// Streamer — generic интерфейс
type Streamer[T any] interface {
	StreamToMinio(ctx context.Context, dataCh <-chan []T, rowConverter func(row T) []interface{}) (string, error)
}

// XlsxStreamer реализует Streamer
type XlsxStreamer[T any] struct {
	Client MinioClient
	Config Config
	Logger logger.Logger
}

// StreamToMinio стримит данные из dataCh в xlsx, загружает в MinIO и возвращает ссылку или ключ
func (s *XlsxStreamer[T]) StreamToMinio(ctx context.Context, dataCh <-chan []T, rowConverter func(row T) []interface{}) (string, error) {
	var t T
	typeName := reflect.TypeOf(t)
	s.Logger.DebugWithCtx(ctx, fmt.Sprintf("%v.StreamToMinio", typeName))

	pr, pw := io.Pipe()

	// Создаем контекст с возможностью отмены для горутины
	goroutineCtx, goroutineCancel := context.WithCancel(ctx)
	defer goroutineCancel() // Гарантированная отмена горутины при выходе

	// Канал для ошибок из горутины
	errCh := make(chan error, 2)

	// WaitGroup для отслеживания горутин
	var wg sync.WaitGroup
	wg.Add(1)

	// Запускаем горутину для создания Excel
	go func() {
		defer func() {
			wg.Done()
			err := pw.Close()
			if err != nil {
				s.Logger.Warn("stream writer. pipe writer close error", logger.Error(err))
			}
		}()

		sw := NewExcelStreamWriter(s.Logger) // Fallback на excelize

		// Флаг для отслеживания закрытия канала
		channelClosed := false

		for !channelClosed {
			select {
			case <-goroutineCtx.Done():
				s.Logger.Warn("context canceled during stream write", logger.Error(goroutineCtx.Err()))
				errCh <- goroutineCtx.Err()
				_ = pw.CloseWithError(goroutineCtx.Err())
				return

			case batch, ok := <-dataCh:
				if !ok {
					// Канал данных закрыт, завершаем запись
					s.Logger.Debug("data channel is closed. finishing stream write")
					channelClosed = true

					if err := sw.Flush(goroutineCtx); err != nil {
						s.Logger.Error("stream writer flush error", logger.Error(err))
						errCh <- errors.Wrap(err, "failed to flush")
						_ = pw.CloseWithError(err)
						return
					}

					if err := sw.WriteFile(goroutineCtx, pw); err != nil {
						s.Logger.Error("failed to write excel", logger.Error(err))
						errCh <- errors.Wrap(err, "failed to write excel")
						_ = pw.CloseWithError(err)
						return
					}

					s.Logger.Debug("excel file written to pipe successfully")
					return
				}

				// Преобразуем batch в формат, который понимает ExcelStreamWriter
				rows := make([][]interface{}, len(batch))

				// Проверяем контекст периодически во время обработки данных
				for i, item := range batch {
					if i > 0 && i%100 == 0 && goroutineCtx.Err() != nil {
						s.Logger.Warn("context canceled during batch processing", logger.Int("processed_items", i))
						errCh <- goroutineCtx.Err()
						_ = pw.CloseWithError(goroutineCtx.Err())
						return
					}

					// Определяем примерное количество колонок на основе первой записи
					if i == 0 && len(batch) > 0 {
						// Получаем тестовый результат конвертации для определения размера
						testRow := rowConverter(item)
						// Для всех остальных строк предвыделяем память нужного размера
						for j := 0; j < len(batch); j++ {
							rows[j] = make([]interface{}, 0, len(testRow))
						}
					}

					// Для очень больших батчей можно выполнять промежуточную запись
					// для снижения требований к памяти
					if i > 1000 && i%1000 == 0 {
						if err := sw.WriteRows(goroutineCtx, rows[:i]); err != nil {
							s.Logger.Error("stream writer intermediate write error", logger.Error(err))
							errCh <- errors.Wrap(err, "failed to write intermediate rows")
							_ = pw.CloseWithError(err)
							return
						}
						// Обнуляем уже записанную часть
						for j := 0; j < i; j++ {
							rows[j] = nil
						}
					}

					// Используем переданный конвертер для получения строки в нужном формате
					rows[i] = rowConverter(item)
				}

				// Попробуйте измерить использование памяти до и после вызовов
				memBefore := new(runtime.MemStats)
				runtime.ReadMemStats(memBefore)

				// Записываем пакет строк через WriteRows с передачей контекста
				if err := sw.WriteRows(goroutineCtx, rows); err != nil {
					s.Logger.Error("stream writer rows write error", logger.Error(err))
					errCh <- errors.Wrap(err, "failed to write rows")
					_ = pw.CloseWithError(err)
					return
				}

				memAfter := new(runtime.MemStats)
				runtime.ReadMemStats(memAfter)
				// fmt.Println("Excel Writer. alloc delta:", memAfter.Alloc-memBefore.Alloc, "TotalAlloc delta:", memAfter.TotalAlloc-memBefore.TotalAlloc)
			}
		}
	}()

	// Загружаем в MinIO
	info, err := s.Client.PutObject(ctx, s.Config.BucketName, s.Config.ObjectName, pr, -1, minio.PutObjectOptions{})

	// Ожидаем завершения горутины с таймаутом
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		s.Logger.Debug("stream writer goroutine finished successfully")
	case <-time.After(300 * time.Millisecond):
		s.Logger.Warn("stream writer goroutine timeout")
	}

	// Проверяем ошибку PutObject
	if err != nil {
		s.Logger.Error("stream writer error on minio PutObject", logger.Error(err))
		return "", fmt.Errorf("put object failed: %w", err)
	}

	// Проверяем ошибки из горутины Excel
	select {
	case err, ok := <-errCh:
		if ok && err != nil {
			s.Logger.Error("stream writer excel goroutine error", logger.Error(err))
			return "", err
		}
	default:
		// Нет ошибок
	}

	_ = pr.Close()

	// Генерируем пресайн URL при необходимости
	if s.Config.PresignExpire > 0 {
		s.Logger.Debug("stream writer presign url generate")
		url, err := s.Client.PresignedGetObject(ctx, s.Config.BucketName, s.Config.ObjectName, s.Config.PresignExpire, nil)
		if err != nil {
			s.Logger.Error("stream writer presign url generation failed", logger.Error(err))
			return "", fmt.Errorf("presign failed: %w", err)
		}
		return url, nil
	}

	return info.Key, nil
}
