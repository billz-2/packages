package main

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"time"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/streamxlsx"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type testRow struct {
	Rows [][]interface{} `json:"rows"`
}

// checking mem allocations on 300000 rows with 50 columns
func main() {
	var mStart, mEnd runtime.MemStats

	// Очистка памяти перед каждым тестом
	runtime.GC()
	runtime.ReadMemStats(&mStart)
	defer func() {
		runtime.ReadMemStats(&mEnd)

		fmt.Println(float64(mEnd.Alloc-mStart.Alloc)/1024/1024, "MB_allocated")
		fmt.Println(float64(mEnd.TotalAlloc-mStart.TotalAlloc)/1024/1024, "MB_total_allocated")
		fmt.Println(float64(mEnd.NumGC-mStart.NumGC), "GC_cycles")
	}()

	client, err := minio.New("localhost:9099", &minio.Options{
		Creds:  credentials.NewStaticV4("2wjyybzuy3g4ednkmpgmhczzdvfbk87d", "tgetjnczdxx9engvkthsy8cy8p25yqmz", ""),
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
