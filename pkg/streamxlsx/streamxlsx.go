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
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/pkg/errors"
	"github.com/xuri/excelize/v2"
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

// StreamToMinio стримит данные из dataCh в xlsx, загружает в MinIO и возвращает ссылку или ключ
func (s *XlsxStreamer[T]) StreamTempToMinio(ctx context.Context, dataCh <-chan T, headers []string, rowConverter func(row T) [][]interface{}) (string, error) {
	var t T
	typeName := reflect.TypeOf(t)
	s.Logger.DebugWithCtx(ctx, fmt.Sprintf("%v.StreamTempToMinio", typeName))

	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Println(err)
		}
	}()

	sw, err := f.NewStreamWriter("Sheet1")
	if err != nil {
		s.Logger.Error("stream writer. excel stream writer failed to create", logger.Error(err))
		return "", errors.Wrap(err, "failed to create stream writer")
	}

	headerCells := make([]interface{}, len(headers))
	for i, header := range headers {
		headerCells[i] = excelize.Cell{Value: header}
	}

	err = sw.SetRow("A1", headerCells)
	if err != nil {
		s.Logger.Error("stream writer error on headers set row", logger.Error(err))
		return "", errors.Wrap(err, "stream writer headers set row failed")
	}

	yAxis := 2

	for data := range dataCh {
		// Проверяем отмену контекста перед операцией
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		rows := rowConverter(data)
		if len(rows) == 0 {
			continue
		}

		for _, row := range rows {
			rowToCells(row)
			cell, err := excelize.CoordinatesToCellName(1, yAxis)
			if err != nil {
				s.Logger.Error("stream writer error on coordinates to cell name", logger.Error(err))
				return "", errors.Wrap(err, "stream writer coordinates to cell name failed")
			}

			if err := sw.SetRow(cell, row); err != nil {
				s.Logger.Error("stream writer error on write", logger.Error(err))
				return "", errors.Wrap(err, "stream writer write failed")
			}

			yAxis++
		}
	}

	if err := sw.Flush(); err != nil {
		s.Logger.Error("stream writer error on flush", logger.Error(err))
		return "", errors.Wrap(err, "stream writer flush failed")
	}

	fileUuid, err := uuid.NewUUID()
	if err != nil {
		s.Logger.Error("failed to generate UUID for file", logger.Error(err))
		return "", errors.Wrap(err, "uuid generation failed")
	}
	tempFileName := fmt.Sprintf("excel-%s.xlsx", fileUuid.String())

	if err := f.SaveAs(tempFileName); err != nil {
		fmt.Println(err)
	}

	defer func() {
		err = os.Remove(tempFileName)
		if err != nil {
			s.Logger.Error("failed to remove temporary file", logger.Error(err))
		}
	}()

	file, err := os.Open(tempFileName)
	defer func() {
		err = file.Close()
		if err != nil {
			s.Logger.Error("failed to close temporary file", logger.Error(err))
		}
	}()

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

		f := excelize.NewFile()
		defer func(f *excelize.File) {
			err := f.Close()
			if err != nil {
				s.Logger.Error("stream writer. excel file close error", logger.Error(err))
			}
		}(f)

		sw, err := f.NewStreamWriter("Sheet1")
		if err != nil {
			_ = pw.CloseWithError(errors.Wrap(err, "failed to create stream writer"))
			return
		}

		// Записываем заголовки
		headerCells := make([]interface{}, len(headers))
		for i, header := range headers {
			headerCells[i] = excelize.Cell{Value: header}
		}

		if err := sw.SetRow("A1", headerCells); err != nil {
			_ = pw.CloseWithError(errors.Wrap(err, "stream writer headers set row failed"))
			return
		}

		yAxis := 2
		rowsCount := 0

		// Читаем данные из канала
		for data := range dataCh {
			select {
			case <-ctx.Done():
				_ = pw.CloseWithError(ctx.Err())
				return
			default:
				// Продолжаем обработку
			}

			rows := rowConverter(data)
			if len(rows) == 0 {
				continue
			}

			for _, row := range rows {
				rowToCells(row)
				cell, err := excelize.CoordinatesToCellName(1, yAxis)
				if err != nil {
					_ = pw.CloseWithError(errors.Wrap(err, "stream writer coordinates to cell name failed"))
					return
				}

				if err := sw.SetRow(cell, row); err != nil {
					_ = pw.CloseWithError(errors.Wrap(err, "stream writer write failed"))
					return
				}

				yAxis++
				rowsCount++
			}

			// Только для логирования прогресса
			if rowsCount > 0 && rowsCount%1000 == 0 {
				s.Logger.Debug(fmt.Sprintf("Processed %d rows", rowsCount))
			}
		}

		if err := sw.Flush(); err != nil {
			_ = pw.CloseWithError(errors.Wrap(err, "stream writer flush failed"))
			return
		}

		if err := f.Write(pw); err != nil {
			_ = pw.CloseWithError(errors.Wrap(err, "failed to write excel file to pipe"))
			return
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

func rowToCells(row []interface{}) {
	for i, val := range row {
		switch v := val.(type) {
		case string, int, int64, float64, bool, nil:
			row[i] = excelize.Cell{Value: v}
		case time.Time:
			// Для дат можно использовать специальный формат
			row[i] = excelize.Cell{
				Value:   v.Format("2006-01-02"),
				StyleID: 0, // ID стиля для даты
			}
		default:
			// Для всех остальных типов используем строковое представление
			row[i] = excelize.Cell{Value: fmt.Sprintf("%v", v)}
		}
	}

	return
}
