package streamxlsx

import (
	"context"
	"fmt"
	"io"
	"reflect"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/minio/minio-go/v7"
	"github.com/pkg/errors"
)

type MinioClient interface {
	PutObject(ctx context.Context, bucketName, objectName string, reader io.Reader, objectSize int64, opts minio.PutObjectOptions) (minio.UploadInfo, error)
	PresignedGetObject(ctx context.Context, bucketName, objectName string, expirySeconds int64, reqParams map[string]string) (string, error)
}

type Config struct {
	BucketName    string
	ObjectName    string
	PresignExpire int64 // в секундах, 0 - не генерировать
}

type Streamer[T any] interface {
	StreamToMinio(ctx context.Context, dataCh <-chan []T, rowConverter func(row T) []interface{}) (string, error)
}

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
	fileRoutineCtx, fileRoutineCancel := context.WithCancel(ctx)

	defer func(reader *io.PipeReader) {
		fileRoutineCancel()
		err := reader.Close()
		if err != nil {
			s.Logger.Error("stream writer pipe reader close error", logger.Error(err))
		}
	}(pr)

	go func() {
		defer func() {
			err := pw.Close()
			if err != nil {
				s.Logger.Warn("stream writer. pipe writer close error", logger.Error(err))
			}
		}()

		sw := NewExcelStreamWriter(s.Logger)

		for {
			select {
			case <-fileRoutineCtx.Done():
				s.Logger.Warn("context canceled during stream write", logger.Error(fileRoutineCtx.Err()))
				_ = pw.CloseWithError(fileRoutineCtx.Err())

				return

			case batch, ok := <-dataCh:
				if !ok {
					s.Logger.Debug("data channel is closed. finishing stream write")

					if err := sw.Flush(fileRoutineCtx); err != nil {
						s.Logger.Error("stream writer flush error", logger.Error(err))
						_ = pw.CloseWithError(err)

						return
					}

					if err := sw.WriteFile(fileRoutineCtx, pw); err != nil {
						s.Logger.Error("stream writer write file error", logger.Error(err))
						_ = pw.CloseWithError(err)

						return
					}

					s.Logger.Debug("excel file written to pipe successfully")
					return
				}

				// Преобразуем batch в формат, который понимает ExcelStreamWriter
				rows := make([][]interface{}, len(batch))
				// Добавляем счетчик для отслеживания фактически записанных строк
				processedCount := 0

				// Проверяем контекст периодически во время обработки данных
				for i, item := range batch {
					if i > 0 && i%100 == 0 && fileRoutineCtx.Err() != nil {
						s.Logger.Warn("context canceled during batch processing", logger.Int("processed_items", i))
						_ = pw.CloseWithError(fileRoutineCtx.Err())

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
					if len(batch) >= 500 && i > 0 && i%500 == 0 {
						if err := sw.WriteRows(fileRoutineCtx, rows[:i-processedCount]); err != nil {
							s.Logger.Error("stream writer intermediate write error", logger.Error(err))
							_ = pw.CloseWithError(err)

							return
						}

						// сбрасывание счетчика для следующей порции
						processedCount = i
					}

					// Используем переданный конвертер для получения строки в нужном формате
					rows[i-processedCount] = rowConverter(item)
				}

				// Записываем остаток пакета строк
				remainingRows := len(batch) - processedCount
				if remainingRows > 0 {
					// Записываем остаток пакет строк через WriteRows с передачей контекста
					if err := sw.WriteRows(fileRoutineCtx, rows[:remainingRows]); err != nil {
						s.Logger.Error("stream writer rows write error", logger.Error(err))
						_ = pw.CloseWithError(err)

						return
					}
				}
			}
		}
	}()

	// Загружаем в MinIO
	info, err := s.Client.PutObject(ctx, s.Config.BucketName, s.Config.ObjectName, pr, -1, minio.PutObjectOptions{})
	if err != nil {
		s.Logger.Error("stream writer error on minio PutObject", logger.Error(err))
		return "", errors.Wrap(err, "minio put object failed")
	}

	// Генерируем пресайн URL при необходимости
	if s.Config.PresignExpire > 0 {
		s.Logger.Debug("stream writer presign url generate")
		url, err := s.Client.PresignedGetObject(ctx, s.Config.BucketName, s.Config.ObjectName, s.Config.PresignExpire, nil)
		if err != nil {
			s.Logger.Error("stream writer presign url generation failed", logger.Error(err))
			return "", errors.Wrap(err, "minio url presign failed")
		}
		return url, nil
	}

	return info.Key, nil
}
