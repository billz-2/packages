package streamxlsx_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/streamxlsx"
)

// fakeClient keeps what PutObject got, or only counts it.
type fakeClient struct {
	discard bool
	body    []byte
	size    int64
}

func (c *fakeClient) PutObject(
	_ context.Context, _, objectName string, reader io.Reader, objectSize int64, _ minio.PutObjectOptions,
) (minio.UploadInfo, error) {
	if c.discard {
		n, err := io.Copy(io.Discard, reader)
		c.size = n

		return minio.UploadInfo{Key: objectName}, err
	}

	body, err := io.ReadAll(reader)
	c.body, c.size = body, objectSize

	return minio.UploadInfo{Key: objectName}, err
}

func (c *fakeClient) PresignedGetObject(
	_ context.Context, _, _ string, _ time.Duration, _ url.Values,
) (*url.URL, error) {
	return &url.URL{}, nil
}

type stream func(s *streamxlsx.XlsxStreamer[[]interface{}], ctx context.Context, ch <-chan []interface{},
	headers []string, conv func([]interface{}) [][]interface{}) (string, error)

var streams = map[string]stream{
	"temp file": (*streamxlsx.XlsxStreamer[[]interface{}]).StreamTempToMinio,
	"pipe":      (*streamxlsx.XlsxStreamer[[]interface{}]).StreamPipeToMinio,
}

func newStreamer(client *fakeClient) *streamxlsx.XlsxStreamer[[]interface{}] {
	return &streamxlsx.XlsxStreamer[[]interface{}]{
		Client: client,
		Config: streamxlsx.Config{BucketName: "bucket", ObjectName: "file.xlsx"},
		Logger: logger.New(logger.LevelError, "streamxlsx_test"),
	}
}

func oneRow(row []interface{}) [][]interface{} { return [][]interface{}{row} }

func send(rows [][]interface{}) <-chan []interface{} {
	ch := make(chan []interface{}, len(rows))
	for _, row := range rows {
		ch <- row
	}

	close(ch)

	return ch
}

// copyRows gives each stream its own rows: the old implementation replaced the values with excelize.Cell.
func copyRows(rows [][]interface{}) [][]interface{} {
	out := make([][]interface{}, len(rows))
	for i, row := range rows {
		out[i] = append([]interface{}(nil), row...)
	}

	return out
}

var (
	headers = []string{"Дата", "ID транзакции", " Магазин ", "Сумма", "Флаг", "Пусто", "Время", "Прочее"}
	rows    = [][]interface{}{
		{"2024-08-08 11:35:04", int64(669432), "Store test-zakaz3", 33000.5, true, nil, time.Date(2024, 8, 8, 0, 0, 0, 0,
			time.UTC), []string{"An A"}},
		{" a<b&\"c\"\n ", 7, "_x0041_ и _x005F_x0041_", math.Inf(1), false, nil, time.Time{}, struct{ A int }{1}},
		{"\tтаб", int64(-1), "строка\r", math.NaN(), true, "", time.Date(2026, 11, 26, 23, 0, 0, 0, time.UTC), 1.5},
		{strings.Repeat("я", excelize.TotalCellChars+10), 0, "", -0.25, false, nil, nil, nil},
		{"short"},
	}
)

// excelizeBook is the xlsx the streamer wrote before: excelize's StreamWriter with the values as excelize.Cell.
func excelizeBook(t *testing.T, headers []string, rows [][]interface{}) []byte {
	t.Helper()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sw, err := f.NewStreamWriter("Sheet1")
	require.NoError(t, err)

	cells := make([]interface{}, len(headers))
	for i, header := range headers {
		cells[i] = excelize.Cell{Value: header}
	}

	require.NoError(t, sw.SetRow("A1", cells))

	for i, row := range rows {
		cells := make([]interface{}, len(row))
		for c, v := range row {
			switch v := v.(type) {
			case string, int, int64, float64, bool, nil:
				cells[c] = excelize.Cell{Value: v}
			case time.Time:
				cells[c] = excelize.Cell{Value: v.Format("2006-01-02")}
			default:
				cells[c] = excelize.Cell{Value: fmt.Sprintf("%v", v)}
			}
		}

		cell, err := excelize.CoordinatesToCellName(1, i+2)
		require.NoError(t, err)
		require.NoError(t, sw.SetRow(cell, cells))
	}

	require.NoError(t, sw.Flush())

	buf, err := f.WriteToBuffer()
	require.NoError(t, err)

	return buf.Bytes()
}

func entries(t *testing.T, book []byte) map[string]string {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(book), int64(len(book)))
	require.NoError(t, err)

	out := make(map[string]string, len(zr.File))

	for _, file := range zr.File {
		r, err := file.Open()
		require.NoError(t, err)

		raw, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())

		out[file.Name] = string(raw)
	}

	return out
}

func TestStream_SameBookAsExcelize(t *testing.T) {
	want := entries(t, excelizeBook(t, headers, copyRows(rows)))

	for name, run := range streams {
		t.Run(name, func(t *testing.T) {
			client := &fakeClient{}

			key, err := run(newStreamer(client), context.Background(), send(copyRows(rows)), headers, oneRow)
			require.NoError(t, err)
			assert.Equal(t, "file.xlsx", key)

			got := entries(t, client.body)
			require.Len(t, got, len(want))

			for part, content := range want {
				assert.Equal(t, content, got[part], part)
			}

			if name == "temp file" {
				assert.Equal(t, int64(len(client.body)), client.size)
			}

			f, err := excelize.OpenReader(bytes.NewReader(client.body))
			require.NoError(t, err)

			read, err := f.GetRows("Sheet1")
			require.NoError(t, err)
			require.Len(t, read, len(rows)+1)
			assert.Equal(t, []string{"2024-08-08 11:35:04", "669432", "Store test-zakaz3", "33000.5", "TRUE", "",
				"2024-08-08", "[An A]"}, read[1])
			assert.Equal(t, []string{"short"}, read[len(rows)])
		})
	}
}

func TestStream_NoRows(t *testing.T) {
	want := entries(t, excelizeBook(t, headers, nil))

	for name, run := range streams {
		t.Run(name, func(t *testing.T) {
			client := &fakeClient{}

			_, err := run(newStreamer(client), context.Background(), send(nil), headers, oneRow)
			require.NoError(t, err)
			assert.Equal(t, want, entries(t, client.body))
		})
	}
}

func TestStream_TempFileRemoved(t *testing.T) {
	before, err := filepath.Glob(filepath.Join(os.TempDir(), "excel-*.xlsx"))
	require.NoError(t, err)

	_, err = newStreamer(&fakeClient{}).StreamTempToMinio(context.Background(), send(copyRows(rows)), headers, oneRow)
	require.NoError(t, err)

	after, err := filepath.Glob(filepath.Join(os.TempDir(), "excel-*.xlsx"))
	require.NoError(t, err)
	assert.ElementsMatch(t, before, after)
}

func TestStream_CancelledContext(t *testing.T) {
	for name, run := range streams {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			_, err := run(newStreamer(&fakeClient{}), ctx, send(copyRows(rows)), headers, oneRow)
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

func TestStream_TooManyRows(t *testing.T) { //nolint:paralleltest // a million rows
	ch := make(chan []interface{})

	go func() {
		defer close(ch)

		for range excelize.TotalRows {
			ch <- []interface{}{1}
		}
	}()

	_, err := newStreamer(&fakeClient{discard: true}).StreamTempToMinio(context.Background(), ch, headers, oneRow)
	require.ErrorIs(t, err, excelize.ErrMaxRows)
}

// TestStream_PeakMemory: the export of a million rows does not keep them, nor the file it makes.
func TestStream_PeakMemory(t *testing.T) { //nolint:paralleltest // HeapInuse of the process is measured
	const (
		rowCount = 1_000_000
		columns  = 20
		limit    = 64 << 20
	)

	heads := make([]string, columns)
	for i := range columns {
		heads[i] = fmt.Sprintf("Column %d", i)
	}

	for name, run := range streams {
		t.Run(name, func(t *testing.T) {
			runtime.GC()

			ch := make(chan []interface{})

			go func() {
				defer close(ch)

				for i := range rowCount {
					row := make([]interface{}, columns)
					for c := range columns {
						if c%2 == 0 {
							row[c] = fmt.Sprintf("value %d of column %d", i%5000, c)
						} else {
							row[c] = float64(i*columns+c) / 4
						}
					}

					ch <- row
				}
			}()

			peak := watchHeapPeak()
			client := &fakeClient{discard: true}

			_, err := run(newStreamer(client), context.Background(), ch, heads, oneRow)
			require.NoError(t, err)

			peakBytes := peak()
			t.Logf("peak HeapInuse of %d rows x %d columns (%d MiB of xlsx): %d MiB",
				rowCount, columns, client.size>>20, peakBytes>>20)
			assert.Less(t, peakBytes, uint64(limit))
		})
	}
}

// watchHeapPeak samples HeapInuse every 50 ms until the returned func is called, which returns the peak.
func watchHeapPeak() func() uint64 {
	var (
		mu   sync.Mutex
		peak uint64
		wg   sync.WaitGroup
	)

	sample := func() {
		var ms runtime.MemStats

		runtime.ReadMemStats(&ms)

		mu.Lock()
		peak = max(peak, ms.HeapInuse)
		mu.Unlock()
	}

	stop := make(chan struct{})

	wg.Add(1)

	go func() {
		defer wg.Done()

		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sample()
			}
		}
	}()

	return func() uint64 {
		close(stop)
		wg.Wait()
		sample()

		mu.Lock()
		defer mu.Unlock()

		return peak
	}
}
