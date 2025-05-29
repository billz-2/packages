package streamxlsx

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"time"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/minio/minio-go/v7"
	"github.com/pkg/errors"
)

type MinioClient interface {
	PutObject(ctx context.Context, bucketName, objectName string, reader io.Reader, objectSize int64, opts minio.PutObjectOptions) (minio.UploadInfo, error)
	PresignedGetObject(ctx context.Context, bucketName, objectName string, expiry time.Duration, reqParams url.Values) (*url.URL, error)
}

type Config struct {
	BucketName    string
	ObjectName    string
	PresignExpire int64 // в секундах, 0 - не генерировать
}

type Streamer[T any] interface {
	StreamToMinio(ctx context.Context, dataCh <-chan T, rowConverter func(row T) [][]interface{}) (string, error)
}

type XlsxStreamer[T any] struct {
	Client MinioClient
	Config Config
	Logger logger.Logger
}

// StreamToMinio стримит данные из dataCh в xlsx, загружает в MinIO и возвращает ссылку или ключ
func (s *XlsxStreamer[T]) StreamToMinio(ctx context.Context, dataCh <-chan T, rowConverter func(row T) [][]interface{}) (string, error) {
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

			case rec, ok := <-dataCh:
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

				// Преобразуем запись с помощью rowConverter
				converted := rowConverter(rec)

				// Добавляем счетчик для отслеживания фактически записанных строк
				processedCount := 0

				// Проверяем контекст периодически во время обработки данных
				for i := range converted {
					if i > 0 && i%100 == 0 && fileRoutineCtx.Err() != nil {
						s.Logger.Warn("context canceled during batch processing", logger.Int("processed_items", i))
						_ = pw.CloseWithError(fileRoutineCtx.Err())

						return
					}

					// Для очень больших батчей можно выполнять промежуточную запись
					// для снижения требований к памяти
					if i > 0 && i%500 == 0 {
						if err := sw.WriteRows(fileRoutineCtx, converted[processedCount:i]); err != nil {
							s.Logger.Error("stream writer intermediate write error", logger.Error(err))
							_ = pw.CloseWithError(err)

							return
						}

						for j := processedCount; j < i; j++ {
							converted[j] = nil
						}

						processedCount = i
					}
				}

				// Записываем остаток пакета строк
				remainingRows := len(converted) - processedCount
				if remainingRows > 0 {
					// Записываем остаток пакет строк через WriteRows с передачей контекста
					if err := sw.WriteRows(fileRoutineCtx, converted[processedCount:]); err != nil {
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
		fUrl, err := s.Client.PresignedGetObject(ctx, s.Config.BucketName, s.Config.ObjectName, time.Duration(s.Config.PresignExpire), nil)
		if err != nil {
			s.Logger.Error("stream writer presign url generation failed", logger.Error(err))
			return "", errors.Wrap(err, "minio url presign failed")
		}
		return fUrl.String(), nil
	}

	return info.Key, nil
}
