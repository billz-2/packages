package streamxlsx

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/xuri/excelize/v2"
)

const FlushInterval = 1000 // Оптимальное значение для промежуточного сброса буферов на синтетических тестах

type ExcelStreamWriter interface {
	WriteHeaders(ctx context.Context, headers []string) error
	WriteRows(ctx context.Context, rows [][]interface{}) error
	Write(ctx context.Context, headers []string, rows [][]interface{}) (string, error)
	Flush(ctx context.Context) error
	WriteFile(ctx context.Context, writer *io.PipeWriter) error
}

type excelStreamWriterService struct {
	axis         AxisGenerator
	sheetName    string
	streamWriter *excelize.StreamWriter
	file         *excelize.File
	logger       logger.Logger
}

func NewExcelStreamWriter(log logger.Logger) ExcelStreamWriter {
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		log.Error("stream writer. excel stream writer failed to create", logger.Error(err))
		return nil
	}

	return &excelStreamWriterService{
		axis:         NewAxisGenerator(1),
		sheetName:    sheet,
		streamWriter: sw,
		file:         f,
		logger:       log,
	}
}

func (s *excelStreamWriterService) Flush(ctx context.Context) error {
	// Проверяем отмену контекста перед операцией
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.streamWriter.Flush()
}

func (s *excelStreamWriterService) WriteFile(ctx context.Context, writer *io.PipeWriter) error {
	// Проверяем отмену контекста перед операцией
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.file.Write(writer)
}

func (s *excelStreamWriterService) Write(ctx context.Context, headers []string, rows [][]interface{}) (string, error) {
	var err error
	method := "excelStreamWriterService.Write"

	s.logger.DebugWithCtx(ctx, method)

	// Периодически проверяем контекст
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	err = s.WriteHeaders(ctx, headers)
	if err != nil {
		s.logger.ErrorWithCtx(ctx, "failed to write headers", logger.Error(err))
		return "", err
	}

	err = s.WriteRows(ctx, rows)
	if err != nil {
		s.logger.ErrorWithCtx(ctx, "failed to write rows", logger.Error(err))
		return "", err
	}

	return s.file.Path, err
}

func (s *excelStreamWriterService) WriteHeaders(ctx context.Context, headers []string) error {
	// Проверяем отмену контекста перед операцией
	if ctx.Err() != nil {
		return ctx.Err()
	}

	axis := s.axis.GetCurrent()
	s.axis.NextRow()
	cells := make([]interface{}, len(headers))

	for i, v := range headers {
		cells[i] = v
	}

	return s.streamWriter.SetRow(axis, cells)
}

func (s *excelStreamWriterService) WriteRows(ctx context.Context, rows [][]interface{}) error {
	// Сбрасывать временные буферы после записи
	defer func() {
		s.clearTemporaryBuffers()
	}()

	// Обрабатываем каждую строку с периодической проверкой контекста
	for i, row := range rows {
		// Проверяем контекст каждые 50 строк
		if i > 0 && i%200 == 0 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}

		// Промежуточный Flush для очистки буферов при обработке больших объемов
		if i > 0 && i%FlushInterval == 0 {
			if err := s.streamWriter.Flush(); err != nil {
				s.logger.Error("failed interim flush", logger.Error(err))
				return err
			}
		}

		err := s.writeRow(row)
		if err != nil {
			return err
		}
	}

	return nil
}

var dataPool = sync.Pool{
	New: func() interface{} {
		return make([]interface{}, 64)
	},
}

func (s *excelStreamWriterService) writeRow(row []interface{}) error {
	axis := s.axis.GetCurrent()
	s.axis.NextRow()

	rawData := dataPool.Get().([]interface{})
	data := rawData[:0]

	// Расширяем при необходимости
	if cap(rawData) < len(row) {
		data = make([]interface{}, len(row))
	} else {
		data = rawData[:len(row)]
	}

	for i, val := range row {
		switch v := val.(type) {
		case string:
		case int:
		case int64:
		case float64:
		case bool:
			data[i] = v
		case time.Time:
			// Для дат можно использовать специальный формат
			data[i] = excelize.Cell{
				Value:   v.Format("2006-01-02"),
				StyleID: 0, // ID стиля для даты
			}
		case nil:
			data[i] = nil // Пустая ячейка
		default:
			// Для всех остальных типов используем строковое представление
			data[i] = fmt.Sprintf("%v", v)
		}
	}

	// Запись данных через StreamWriter
	if err := s.streamWriter.SetRow(axis, data); err != nil {
		s.logger.Error("failed to write row", logger.Error(err))
		return err
	}

	// Очищаем и возвращаем в пул
	for i := range data {
		data[i] = nil
	}
	dataPool.Put(data[:0])

	return nil
}

// Метод для очистки временных буферов после записи
func (s *excelStreamWriterService) clearTemporaryBuffers() {
	// Excelize не предоставляет прямого API для очистки внутренних буферов
	// Но можно вызвать промежуточный Flush для освобождения части памяти
	// при записи больших объемов данных
	if err := s.streamWriter.Flush(); err != nil {
		s.logger.Error("failed to flush temporary buffers", logger.Error(err))
	}

	// Принудительно запускаем GC в критических местах для диагностики
	// Не рекомендуется использовать в production коде!
	// runtime.GC()
}
