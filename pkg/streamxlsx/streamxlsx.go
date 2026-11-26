package streamxlsx

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"time"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/minio/minio-go/v7"
	"github.com/pkg/errors"
)

const excelContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

type MinioClient interface {
	PutObject(ctx context.Context, bucketName, objectName string, reader io.Reader, objectSize int64, opts minio.PutObjectOptions) (minio.UploadInfo, error)
	PresignedGetObject(ctx context.Context, bucketName, objectName string, expiry time.Duration, reqParams url.Values) (*url.URL, error)
}

type Config struct {
	BucketName    string
	ObjectName    string
	PresignExpire time.Duration // в секундах, 0 - не генерировать
}

type Streamer[T any] interface {
	StreamTempToMinio(ctx context.Context, dataCh <-chan T, headers []string, rowConverter func(row T) [][]interface{}) (string, error)
}

type XlsxStreamer[T any] struct {
	Client MinioClient
	Config Config
	Logger logger.Logger
}

// StreamTempToMinio стримит данные из dataCh в xlsx во временном файле, загружает его в MinIO и возвращает ссылку
// или ключ. Память не растёт с числом строк: файл пишется по мере прихода строк (sheetWriter).
func (s *XlsxStreamer[T]) StreamTempToMinio(ctx context.Context, dataCh <-chan T, headers []string, rowConverter func(row T) [][]interface{}) (string, error) {
	var t T
	typeName := reflect.TypeOf(t)
	s.Logger.DebugWithCtx(ctx, fmt.Sprintf("%v.StreamTempToMinio", typeName))

	file, err := os.CreateTemp("", "excel-*.xlsx")
	if err != nil {
		s.Logger.Error("failed to create temporary file", logger.Error(err))
		return "", errors.Wrap(err, "temp file create failed")
	}

	defer func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			s.Logger.Error("failed to close temporary file", logger.Error(err))
		}

		if err := os.Remove(file.Name()); err != nil {
			s.Logger.Error("failed to remove temporary file", logger.Error(err))
		}
	}()

	if err := s.writeXlsx(ctx, file, dataCh, headers, rowConverter); err != nil {
		return "", err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		s.Logger.Error("failed to rewind temporary file", logger.Error(err))
		return "", errors.Wrap(err, "temp file seek failed")
	}

	fileStat, err := file.Stat()
	if err != nil {
		s.Logger.Error("failed to get file info", logger.Error(err))
		return "", errors.Wrap(err, "file stat failed")
	}

	// Загружаем в MinIO
	info, err := s.Client.PutObject(
		ctx,
		s.Config.BucketName,
		s.Config.ObjectName,
		file,
		fileStat.Size(),
		minio.PutObjectOptions{ContentType: excelContentType},
	)
	if err != nil {
		s.Logger.Error("stream writer error on minio PutObject", logger.Error(err))
		return "", errors.Wrap(err, "minio put object failed")
	}

	// Генерируем пресайн URL при необходимости
	if s.Config.PresignExpire > time.Second {
		s.Logger.Debug("stream writer presign url generate")
		fUrl, err := s.Client.PresignedGetObject(ctx, s.Config.BucketName, s.Config.ObjectName, s.Config.PresignExpire, nil)
		if err != nil {
			s.Logger.Error("stream writer presign url generation failed", logger.Error(err))
			return "", errors.Wrap(err, "minio url presign failed")
		}
		return fUrl.String(), nil
	}

	return info.Key, nil
}

func (s *XlsxStreamer[T]) StreamPipeToMinio(ctx context.Context, dataCh <-chan T, headers []string, rowConverter func(row T) [][]interface{}) (string, error) {
	var t T
	typeName := reflect.TypeOf(t)
	s.Logger.DebugWithCtx(ctx, fmt.Sprintf("%v.StreamPipeToMinio", typeName))

	// Создаем pipe для передачи данных
	pr, pw := io.Pipe()

	// Запускаем горутину для записи Excel
	go func() {
		defer func(pw *io.PipeWriter) {
			err := pw.Close()
			if err != nil {
				s.Logger.Warn("stream writer. pipe writer close error", logger.Error(err))
			}
		}(pw)

		if err := s.writeXlsx(ctx, pw, dataCh, headers, rowConverter); err != nil {
			_ = pw.CloseWithError(err)
		}
	}()

	// Загружаем данные из pipe в MinIO
	info, err := s.Client.PutObject(
		ctx,
		s.Config.BucketName,
		s.Config.ObjectName,
		pr,
		-1, // Размер неизвестен из-за потоковой передачи
		minio.PutObjectOptions{ContentType: excelContentType},
	)

	// Проверяем ошибку загрузки
	if err != nil {
		s.Logger.Error("stream writer error on minio PutObject", logger.Error(err))
		return "", errors.Wrap(err, "minio put object failed")
	}

	// Генерируем пресайн URL при необходимости
	if s.Config.PresignExpire > time.Second {
		s.Logger.Debug("stream writer presign url generate")
		fUrl, err := s.Client.PresignedGetObject(ctx, s.Config.BucketName, s.Config.ObjectName, s.Config.PresignExpire, nil)
		if err != nil {
			s.Logger.Error("stream writer presign url generation failed", logger.Error(err))
			return "", errors.Wrap(err, "minio url presign failed")
		}
		return fUrl.String(), nil
	}

	return info.Key, nil
}

// writeXlsx writes the xlsx of the headers and the rows of dataCh to w as the rows come.
func (s *XlsxStreamer[T]) writeXlsx(
	ctx context.Context, w io.Writer, dataCh <-chan T, headers []string, rowConverter func(row T) [][]interface{},
) error {
	sw, err := newSheetWriter(w, headers)
	if err != nil {
		s.Logger.Error("stream writer. excel stream writer failed to create", logger.Error(err))
		return err
	}

	rowsCount := 0

	for data := range dataCh {
		// Проверяем отмену контекста перед операцией
		if ctx.Err() != nil {
			return ctx.Err()
		}

		for _, row := range rowConverter(data) {
			if err := sw.WriteRow(row); err != nil {
				s.Logger.Error("stream writer error on write", logger.Error(err))
				return errors.Wrap(err, "stream writer write failed")
			}

			rowsCount++
		}

		// Только для логирования прогресса
		if rowsCount > 0 && rowsCount%1000 == 0 {
			s.Logger.Debug(fmt.Sprintf("Processed %d rows", rowsCount))
		}
	}

	if err := sw.Close(); err != nil {
		s.Logger.Error("stream writer error on flush", logger.Error(err))
		return errors.Wrap(err, "stream writer flush failed")
	}

	return nil
}
