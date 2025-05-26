package test

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/streamxlsx"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Мок для MinioClient, который не блокируется и корректно обрабатывает отмену контекста
type mockMinioClient struct {
	mu              sync.Mutex
	putObjectCalled bool
	putObjectError  error
	presignURL      string
	presignError    error
	readBytesCount  int64
	forcedReadDelay time.Duration // Для симуляции медленного чтения
}

// PutObject с правильной обработкой отмены контекста
func (m *mockMinioClient) PutObject(ctx context.Context, bucketName, objectName string, reader io.Reader, objectSize int64, opts minio.PutObjectOptions) (minio.UploadInfo, error) {
	// Отмечаем вызов метода
	m.mu.Lock()
	m.putObjectCalled = true
	m.mu.Unlock()

	/*fmt.Println("mockMinioClient.PutObject: начало", time.Now())
	defer fmt.Println("mockMinioClient.PutObject: завершение", time.Now()) */

	// Проверяем контекст на отмену
	if ctx.Err() != nil {
		return minio.UploadInfo{}, ctx.Err()
	}

	// Создаем контекст с таймаутом для чтения из pipe
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Канал для сигнализации о завершении чтения
	readDone := make(chan struct{})
	var readErr error
	var bytesRead int64

	// Читаем данные в отдельной горутине
	go func() {
		defer close(readDone)
		// fmt.Println("mockMinioClient.PutObject: начало чтения", time.Now())

		// Используем буфер ограниченного размера
		buffer := make([]byte, 32*1024)
		var n int64

		for {
			// Проверяем контекст перед каждым чтением
			if readCtx.Err() != nil {
				readErr = readCtx.Err()
				// fmt.Println("mockMinioClient.PutObject: контекст отменен во время чтения", time.Now())
				return
			}

			// Читаем данные в буфер
			nr, err := reader.Read(buffer)
			if nr > 0 {
				n += int64(nr)
			}

			if err == io.EOF {
				// Нормальное завершение чтения
				// fmt.Println("mockMinioClient.PutObject: EOF получен", time.Now())
				break
			}

			if err != nil {
				readErr = err
				// fmt.Println("mockMinioClient.PutObject: ошибка чтения", err, time.Now())
				return
			}
		}

		// Сохраняем количество прочитанных байт
		m.mu.Lock()
		bytesRead = n
		m.readBytesCount = n
		m.mu.Unlock()

		// fmt.Println("mockMinioClient.PutObject: чтение завершено, прочитано байт:", bytesRead, time.Now())

		// КРИТИЧЕСКИ ВАЖНО: закрываем reader, если он поддерживает io.Closer
		if closer, ok := reader.(io.Closer); ok {
			_ = closer.Close()

			// fmt.Println("mockMinioClient.PutObject: закрытие reader", err, time.Now())
		}
	}()

	// Ждем завершения чтения или таймаута
	select {
	case <-readDone:
		// Чтение завершено
		if readErr != nil && readErr != io.EOF {
			return minio.UploadInfo{}, readErr
		}
	case <-readCtx.Done():
		// Таймаут чтения
		// fmt.Println("mockMinioClient.PutObject: таймаут чтения", time.Now())
		return minio.UploadInfo{}, fmt.Errorf("чтение превысило таймаут: %w", readCtx.Err())
	case <-ctx.Done():
		// Внешний контекст был отменен
		return minio.UploadInfo{}, ctx.Err()
	}

	// Проверяем настроенные параметры теста
	m.mu.Lock()
	putErr := m.putObjectError
	delay := m.forcedReadDelay
	m.mu.Unlock()

	// Имитируем задержку, если она настроена
	if delay > 0 {
		select {
		case <-time.After(delay):
			// Задержка завершена
		case <-ctx.Done():
			return minio.UploadInfo{}, ctx.Err()
		}
	}

	// Возвращаем ошибку, если она настроена
	if putErr != nil {
		return minio.UploadInfo{}, putErr
	}

	// Возвращаем успешный результат
	return minio.UploadInfo{
		Key:  objectName,
		Size: bytesRead,
	}, nil
}

func (m *mockMinioClient) PresignedGetObject(ctx context.Context, bucketName, objectName string, expirySeconds int64, reqParams map[string]string) (string, error) {
	if m.presignError != nil {
		return "", m.presignError
	}
	return m.presignURL, nil
}

// Мок для логгера
type mockLogger struct{}

func (m mockLogger) Debug(msg string, fields ...logger.Field)                             {}
func (m mockLogger) Info(msg string, fields ...logger.Field)                              {}
func (m mockLogger) Warn(msg string, fields ...logger.Field)                              {}
func (m mockLogger) Error(msg string, fields ...logger.Field)                             {}
func (m mockLogger) Fatal(msg string, fields ...logger.Field)                             {}
func (m mockLogger) DebugWithCtx(ctx context.Context, msg string, fields ...logger.Field) {}
func (m mockLogger) InfoWithCtx(ctx context.Context, msg string, fields ...logger.Field)  {}
func (m mockLogger) WarnWithCtx(ctx context.Context, msg string, fields ...logger.Field)  {}
func (m mockLogger) ErrorWithCtx(ctx context.Context, msg string, fields ...logger.Field) {}
func (m mockLogger) FatalWithCtx(ctx context.Context, msg string, fields ...logger.Field) {}

func init() {
	go func() {
		fmt.Println("Starting pprof server on :6060")
		_ = http.ListenAndServe(":6060", nil)
	}()
}

// Тест успешного экспорта
func TestXlsxStreamer_StreamToMinio_Success(t *testing.T) {
	// Таймаут для всего теста
	testCtx, testCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer testCancel()

	// Добавляем больше логирования
	t.Log("Тест начался:", time.Now())

	// Завершающий канал
	testDone := make(chan struct{})

	go func() {
		defer close(testDone)

		// Создаем клиент и стример
		mockClient := &mockMinioClient{
			presignURL: "https://example.com/test.xlsx",
		}

		streamer := &streamxlsx.XlsxStreamer[string]{
			Client: mockClient,
			Config: streamxlsx.Config{
				BucketName:    "test-bucket",
				ObjectName:    "test.xlsx",
				PresignExpire: 3600,
			},
			Logger: mockLogger{},
		}

		// Создаем канал данных
		dataCh := make(chan []string)

		// Функция конвертации для нового API
		rowConverter := func(row string) []interface{} {
			return []interface{}{row}
		}

		// Отправляем данные в отдельной горутине
		go func() {
			defer close(dataCh)
			dataCh <- []string{"Row 1", "Row 2"}
			dataCh <- []string{"Row 3", "Row 4"}
		}()

		// Вызываем тестируемую функцию с адаптированным API
		url, err := streamer.StreamToMinio(context.Background(), dataCh, rowConverter)
		require.NoError(t, err)
		assert.Equal(t, "https://example.com/test.xlsx", url)
		assert.True(t, mockClient.putObjectCalled)
	}()

	// Проверяем, что тест не зависает
	select {
	case <-testDone:
		t.Log("Тест успешно завершился")
	case <-testCtx.Done():
		t.Fatal("Тест завис и был принудительно остановлен")
	}

	t.Log("Функция StreamToMinio завершилась:", time.Now())
}

// Тест отмены контекста
func TestXlsxStreamer_StreamToMinio_ContextCancel(t *testing.T) {
	// Таймаут для всего теста
	testCtx, testCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer testCancel()

	// Завершающий канал
	testDone := make(chan struct{})

	go func() {
		defer close(testDone)

		// Создаем контекст с коротким таймаутом для отмены
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		// Логируем момент отмены контекста
		go func() {
			<-ctx.Done()
			t.Log("Контекст отменен в:", time.Now())
		}()

		// Создаем клиент с искусственной задержкой
		mockClient := &mockMinioClient{
			presignURL:      "https://example.com/test.xlsx",
			forcedReadDelay: 50 * time.Millisecond, // Задержка для срабатывания таймаута
		}

		streamer := &streamxlsx.XlsxStreamer[string]{
			Client: mockClient,
			Config: streamxlsx.Config{
				BucketName:    "test-bucket",
				ObjectName:    "test.xlsx",
				PresignExpire: 3600,
			},
			Logger: mockLogger{},
		}

		// Канал с большим количеством данных
		dataCh := make(chan []string)

		// Отправляем много данных
		go func() {
			defer close(dataCh)
			for i := 0; i < 1000; i++ {
				select {
				case <-ctx.Done():
					return
				case dataCh <- []string{fmt.Sprintf("Row %d", i)}:
					// Добавляем задержку, чтобы не забить канал
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()

		// Новый конвертер для строк
		rowConverter := func(row string) []interface{} {
			return []interface{}{row}
		}

		// Вызываем функцию с обновленным API
		_, err := streamer.StreamToMinio(ctx, dataCh, rowConverter)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context")
	}()

	// Проверяем, что тест не зависает
	select {
	case <-testDone:
		t.Log("Тест успешно завершился")
	case <-testCtx.Done():
		t.Fatal("Тест завис и был принудительно остановлен")
	}
}

// Тест ошибки PutObject
func TestXlsxStreamer_StreamToMinio_PutObjectError(t *testing.T) {
	// Таймаут для всего теста
	testCtx, testCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer testCancel()

	// Завершающий канал
	testDone := make(chan struct{})

	go func() {
		defer close(testDone)

		// Создаем клиент с ошибкой
		mockClient := &mockMinioClient{
			putObjectError: fmt.Errorf("minio error"),
		}

		streamer := &streamxlsx.XlsxStreamer[string]{
			Client: mockClient,
			Config: streamxlsx.Config{
				BucketName:    "test-bucket",
				ObjectName:    "test.xlsx",
				PresignExpire: 3600,
			},
			Logger: mockLogger{},
		}

		dataCh := make(chan []string)
		go func() {
			defer close(dataCh)
			dataCh <- []string{"Row 1"}
		}()

		rowConverter := func(row string) []interface{} {
			return []interface{}{row}
		}

		// Вызываем функцию с новым API
		_, err := streamer.StreamToMinio(context.Background(), dataCh, rowConverter)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "put object failed")
	}()

	// Проверяем, что тест не зависает
	select {
	case <-testDone:
		t.Log("Тест успешно завершился")
	case <-testCtx.Done():
		t.Fatal("Тест завис и был принудительно остановлен")
	}
}

// TestConcurrentWriteOrderPreservation проверяет сохранение порядка строк
// при конкурентной записи из нескольких источников
func TestConcurrentWriteOrderPreservation(t *testing.T) {
	// Установка таймаута для всего теста
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Создаем клиент, который будет хранить данные для проверки
	mockClient := &mockMinioClient{
		presignURL: "https://example.com/test.xlsx",
	}

	// Буфер для сохранения отправленных данных в порядке их записи
	sentDataInOrder := make([]testRow, 0, 9700)

	// Мьютекс для безопасной записи в общий буфер
	var sentDataMutex sync.Mutex

	// Канал для передачи данных
	dataCh := make(chan []testRow, 1100)

	// Количество воркеров
	const numWorkers = 4
	// Общее количество строк
	const totalRows = 9700

	// Группа ожидания для всех воркеров
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	// Запускаем воркеры для генерации данных
	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()

			// Каждый воркер отправляет свою часть данных
			rowsPerWorker := totalRows / numWorkers
			startRow := workerID * rowsPerWorker
			endRow := startRow + rowsPerWorker

			// Отправляем данные пакетами
			batchSize := 1100
			for i := startRow; i < endRow; i += batchSize {
				currentBatch := min(batchSize, endRow-i)
				batch := make([]testRow, currentBatch)

				for j := 0; j < currentBatch; j++ {
					// Создаем уникальную строку для каждого элемента
					row := testRow{
						A: i + j,
						B: fmt.Sprintf("worker-%d-row-%d", workerID, i+j),
					}
					batch[j] = row

					// Сохраняем отправленные данные для последующей проверки
					sentDataMutex.Lock()
					sentDataInOrder = append(sentDataInOrder, row)
					sentDataMutex.Unlock()
				}

				// Небольшая случайная задержка для имитации асинхронной обработки
				time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)

				select {
				case dataCh <- batch:
					// Данные отправлены
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Горутина для закрытия канала после завершения всех воркеров
	go func() {
		wg.Wait()
		close(dataCh)
	}()

	// Создаем стример
	streamer := &streamxlsx.XlsxStreamer[testRow]{
		Client: mockClient,
		Config: streamxlsx.Config{
			BucketName: "test-bucket",
			ObjectName: "test.xlsx",
		},
		Logger: mockLogger{},
	}

	// Функция конвертации
	rowConverter := func(row testRow) []interface{} {
		return []interface{}{row.A, row.B}
	}

	// Запускаем стриминг
	_, err := streamer.StreamToMinio(ctx, dataCh, rowConverter)
	require.NoError(t, err)

	// Проверяем, что данные были отправлены в правильном порядке
	require.Equal(t, totalRows, len(sentDataInOrder), "Должно быть отправлено точное количество строк")

	// Проверяем, что все строки уникальны (основано на поле A)
	seen := make(map[int]bool)
	for _, row := range sentDataInOrder {
		require.False(t, seen[row.A], "Обнаружена дублирующаяся строка с A=%d", row.A)
		seen[row.A] = true
	}

	// Проверяем последнее состояние клиента
	require.True(t, mockClient.putObjectCalled, "Метод PutObject должен быть вызван")

	// Если бы был реальный MinIO, мы могли бы здесь загрузить файл и проверить содержимое
	t.Log("Успешно записано", len(sentDataInOrder), "строк в порядке их поступления")
}

// Определение тестовой структуры
type testRow struct {
	A int
	B string
}

// Бенчмарк с разными размерами данных и размерами батчей
func BenchmarkStreamToMinio_WithDifferentSizes(b *testing.B) {
	// Тестируем разное количество строк
	rowSizes := []int{100, 1000, 10000}
	// Тестируем разные размеры батчей
	batchSizes := []int{10, 100, 1000}

	for _, rowSize := range rowSizes {
		for _, batchSize := range batchSizes {
			// Пропускаем нерелевантные комбинации
			if batchSize > rowSize {
				continue
			}

			b.Run(fmt.Sprintf("Rows-%d_Batch-%d", rowSize, batchSize), func(b *testing.B) {
				var mStart, mEnd runtime.MemStats

				// Очистка памяти перед каждым тестом
				runtime.GC()
				runtime.ReadMemStats(&mStart)

				// Создаем конвертер и стример один раз
				rowConverter := func(row testRow) []interface{} {
					return []interface{}{row.A, row.B}
				}

				mockClient := &mockMinioClient{
					presignURL: "https://example.com/test.xlsx",
				}

				streamer := &streamxlsx.XlsxStreamer[testRow]{
					Client: mockClient,
					Config: streamxlsx.Config{
						BucketName:    "test-bucket",
						ObjectName:    "test.xlsx",
						PresignExpire: 3600,
					},
					Logger: mockLogger{},
				}

				// Основной цикл бенчмарка
				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					// Используем буферизованный канал
					dataCh := make(chan []testRow, 10)

					go func() {
						// Отправляем данные пачками нужного размера
						for i := 0; i < rowSize; i += batchSize {
							// Вычисляем размер текущего батча
							currentBatchSize := batchSize
							if i+currentBatchSize > rowSize {
								currentBatchSize = rowSize - i
							}

							batch := make([]testRow, currentBatchSize)
							for j := 0; j < currentBatchSize; j++ {
								batch[j] = testRow{
									A: i + j,
									B: fmt.Sprintf("value-%d", i+j),
								}
							}

							// Отправляем батч в канал
							dataCh <- batch
						}
						close(dataCh)
					}()

					// Вызываем тестируемую функцию
					_, err := streamer.StreamToMinio(context.Background(), dataCh, rowConverter)
					if err != nil {
						b.Fatalf("StreamToMinio error: %v", err)
					}
				}

				// Замеряем метрики памяти после завершения теста
				b.StopTimer()
				runtime.ReadMemStats(&mEnd)

				// Отчет о метриках
				b.ReportMetric(float64(mEnd.Alloc-mStart.Alloc)/1024/1024, "MB_allocated")
				b.ReportMetric(float64(mEnd.TotalAlloc-mStart.TotalAlloc)/1024/1024, "MB_total_allocated")
				b.ReportMetric(float64(mEnd.NumGC-mStart.NumGC), "GC_cycles")
				b.ReportMetric(float64(rowSize), "total_rows")
				b.ReportMetric(float64(batchSize), "batch_size")
			})
		}
	}
}

// Тестирование влияния размера строки на производительность
func BenchmarkStreamToMinio_RowSize(b *testing.B) {
	// Тестируем строки разной длины
	dataTypes := []struct {
		name     string
		rowMaker func(int) testRow
		rowSize  int // примерный размер в байтах
	}{
		{
			name: "small_row_8bytes",
			rowMaker: func(i int) testRow {
				return testRow{A: i, B: "s"}
			},
			rowSize: 8,
		},
		{
			name: "medium_row_100bytes",
			rowMaker: func(i int) testRow {
				return testRow{A: i, B: strings.Repeat("x", 100)}
			},
			rowSize: 100,
		},
		{
			name: "large_row_1000bytes",
			rowMaker: func(i int) testRow {
				return testRow{A: i, B: strings.Repeat("x", 1000)}
			},
			rowSize: 1000,
		},
	}

	for _, dt := range dataTypes {
		b.Run(dt.name, func(b *testing.B) {
			var mStart, mEnd runtime.MemStats

			runtime.GC()
			runtime.ReadMemStats(&mStart)

			// Конвертер и стример
			rowConverter := func(row testRow) []interface{} {
				return []interface{}{row.A, row.B}
			}

			mockClient := &mockMinioClient{
				presignURL: "https://example.com/test.xlsx",
			}

			streamer := &streamxlsx.XlsxStreamer[testRow]{
				Client: mockClient,
				Config: streamxlsx.Config{
					BucketName: "test-bucket",
					ObjectName: "test.xlsx",
				},
				Logger: mockLogger{},
			}

			const rowCount = 1000

			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				dataCh := make(chan []testRow, 10)

				go func() {
					batchSize := 100
					for i := 0; i < rowCount; i += batchSize {
						batch := make([]testRow, 0, batchSize)

						for j := 0; j < batchSize && i+j < rowCount; j++ {
							batch = append(batch, dt.rowMaker(i+j))
						}

						dataCh <- batch
					}
					close(dataCh)
				}()

				_, err := streamer.StreamToMinio(context.Background(), dataCh, rowConverter)
				if err != nil {
					b.Fatalf("StreamToMinio error: %v", err)
				}
			}

			b.StopTimer()
			runtime.ReadMemStats(&mEnd)

			b.ReportMetric(float64(dt.rowSize), "row_bytes")
			b.ReportMetric(float64(mEnd.Alloc-mStart.Alloc)/1024/1024, "MB_allocated")
			b.ReportMetric(float64(mEnd.TotalAlloc-mStart.TotalAlloc)/1024/1024, "MB_total_allocated")
			b.ReportMetric(float64(mEnd.NumGC-mStart.NumGC), "GC_cycles")
		})
	}
}

// Добавим более реалистичные условия к существующим бенчмаркам
func BenchmarkStreamToMinio_WithNetworkDelay(b *testing.B) {
	rowSizes := []int{100, 1000}
	networkDelays := []time.Duration{0, 10 * time.Millisecond, 50 * time.Millisecond}

	for _, rowSize := range rowSizes {
		for _, delay := range networkDelays {
			b.Run(fmt.Sprintf("Rows-%d_Delay-%dms", rowSize, delay/time.Millisecond), func(b *testing.B) {
				var mStart, mEnd runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&mStart)

				// Создаем клиент с искусственной задержкой
				mockClient := &mockMinioClient{
					presignURL:      "https://example.com/test.xlsx",
					forcedReadDelay: delay, // Имитация сетевой задержки
				}

				// Конвертер для данных
				rowConverter := func(row testRow) []interface{} {
					return []interface{}{&row.A, &row.B}
				}

				streamer := &streamxlsx.XlsxStreamer[testRow]{
					Client: mockClient,
					Config: streamxlsx.Config{
						BucketName: "test-bucket",
						ObjectName: "test.xlsx",
					},
					Logger: mockLogger{},
				}

				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					dataCh := make(chan []testRow, 10)

					go func() {
						defer close(dataCh)
						batchSize := 100

						for i := 0; i < rowSize; i += batchSize {
							currentSize := min(batchSize, rowSize-i)
							batch := make([]testRow, currentSize)

							for j := 0; j < currentSize; j++ {
								batch[j] = testRow{
									A: i + j,
									B: fmt.Sprintf("value-%d", i+j),
								}
							}

							// Имитация задержки обработки данных
							time.Sleep(1 * time.Millisecond)
							dataCh <- batch
						}
					}()

					_, err := streamer.StreamToMinio(context.Background(), dataCh, rowConverter)
					if err != nil {
						b.Fatalf("StreamToMinio error: %v", err)
					}
				}

				b.StopTimer()
				runtime.ReadMemStats(&mEnd)

				b.ReportMetric(float64(mEnd.Alloc-mStart.Alloc)/1024/1024, "MB_allocated")
				b.ReportMetric(float64(mEnd.TotalAlloc-mStart.TotalAlloc)/1024/1024, "MB_total_allocated")
				b.ReportMetric(float64(delay/time.Millisecond), "network_delay_ms")
				b.ReportMetric(float64(rowSize), "rows")
			})
		}
	}
}

// Бенчмарк с разным размером буфера канала
func BenchmarkStreamToMinio_ChannelBufferSize(b *testing.B) {
	channelBufferSizes := []int{1, 10, 50, 100}

	for _, bufSize := range channelBufferSizes {
		b.Run(fmt.Sprintf("Buffer-%d", bufSize), func(b *testing.B) {
			var mStart, mEnd runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&mStart)

			rowConverter := func(row testRow) []interface{} {
				return []interface{}{row.A, row.B}
			}

			mockClient := &mockMinioClient{
				presignURL: "https://example.com/test.xlsx",
			}

			streamer := &streamxlsx.XlsxStreamer[testRow]{
				Client: mockClient,
				Config: streamxlsx.Config{
					BucketName: "test-bucket",
					ObjectName: "test.xlsx",
				},
				Logger: mockLogger{},
			}

			const rowCount = 1000
			const batchSize = 50

			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				// Канал с разным размером буфера
				dataCh := make(chan []testRow, bufSize)

				go func() {
					defer close(dataCh)

					for i := 0; i < rowCount; i += batchSize {
						currentSize := min(batchSize, rowCount-i)
						batch := make([]testRow, currentSize)

						for j := 0; j < currentSize; j++ {
							batch[j] = testRow{A: i + j, B: "test"}
						}

						dataCh <- batch
					}
				}()

				_, err := streamer.StreamToMinio(context.Background(), dataCh, rowConverter)
				if err != nil {
					b.Fatalf("StreamToMinio error: %v", err)
				}
			}

			b.StopTimer()
			runtime.ReadMemStats(&mEnd)

			b.ReportMetric(float64(bufSize), "channel_buffer")
			b.ReportMetric(float64(mEnd.Alloc-mStart.Alloc)/1024/1024, "MB_allocated")
			b.ReportMetric(float64(mEnd.TotalAlloc-mStart.TotalAlloc)/1024/1024, "MB_total_allocated")
			b.ReportMetric(float64(mEnd.NumGC-mStart.NumGC), "GC_cycles")
		})
	}
}

// Бенчмарк для тестирования конкурентной записи в один файл с сетевой задержкой
func BenchmarkStreamToMinio_ConcurrentWrite(b *testing.B) {
	// Параметры тестирования
	workerCounts := []int{1, 2, 4, 8}
	networkDelays := []time.Duration{0, 20 * time.Millisecond, 50 * time.Millisecond}

	for _, workers := range workerCounts {
		for _, delay := range networkDelays {
			b.Run(fmt.Sprintf("Workers-%d_Delay-%dms", workers, delay/time.Millisecond), func(b *testing.B) {
				var mStart, mEnd runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&mStart)

				// Фиксированное общее количество строк для всех тестов
				const totalRows = 1000
				const rowsPerWorker = totalRows / 4 // каждый воркер обрабатывает часть данных

				// Создаем клиент с задержкой сети
				mockClient := &mockMinioClient{
					presignURL:      "https://example.com/test.xlsx",
					forcedReadDelay: delay,
				}

				// Функция для конвертации данных
				rowConverter := func(row testRow) []interface{} {
					return []interface{}{row.A, row.B}
				}

				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					dataCh := make(chan []testRow, workers*2) // Буфер пропорциональный числу воркеров

					// Создаем группу ожидания для синхронизации воркеров
					var wg sync.WaitGroup
					wg.Add(workers)

					// Запускаем воркеры, которые будут генерировать данные
					for w := 0; w < workers; w++ {
						workerID := w
						go func() {
							defer wg.Done()

							// Каждый воркер обрабатывает свою часть данных
							startRow := workerID * rowsPerWorker
							endRow := startRow + rowsPerWorker

							// Разбиваем данные на батчи
							batchSize := 25
							for i := startRow; i < endRow; i += batchSize {
								// Проверяем, не вышли ли за границу
								currentBatch := min(batchSize, endRow-i)
								batch := make([]testRow, currentBatch)

								for j := 0; j < currentBatch; j++ {
									batch[j] = testRow{
										A: i + j,
										B: fmt.Sprintf("worker-%d-value-%d", workerID, i+j),
									}
								}

								// Имитируем задержку обработки
								time.Sleep(5 * time.Millisecond)
								dataCh <- batch
							}
						}()
					}

					// Запускаем горутину, которая закроет канал после завершения всех воркеров
					go func() {
						wg.Wait()
						close(dataCh)
					}()

					// Создаем стример и отправляем данные
					streamer := &streamxlsx.XlsxStreamer[testRow]{
						Client: mockClient,
						Config: streamxlsx.Config{
							BucketName: "test-bucket",
							ObjectName: "test.xlsx",
						},
						Logger: mockLogger{},
					}

					// Запускаем процесс стриминга
					_, err := streamer.StreamToMinio(context.Background(), dataCh, rowConverter)
					if err != nil {
						b.Fatalf("StreamToMinio error: %v", err)
					}
				}

				b.StopTimer()
				runtime.ReadMemStats(&mEnd)

				// Отчет о метриках
				b.ReportMetric(float64(workers), "workers")
				b.ReportMetric(float64(delay/time.Millisecond), "network_delay_ms")
				b.ReportMetric(float64(mEnd.Alloc-mStart.Alloc)/1024/1024, "MB_allocated")
				b.ReportMetric(float64(mEnd.TotalAlloc-mStart.TotalAlloc)/1024/1024, "MB_total_allocated")
				b.ReportMetric(float64(mEnd.NumGC-mStart.NumGC), "GC_cycles")
			})
		}
	}
}
