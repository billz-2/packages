package compress_test

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"testing"

	"github.com/billz-2/packages/pkg/compress"
	"github.com/stretchr/testify/require"
)

var algorithms = []compress.Algorithm{compress.Zstd, compress.Gzip}

func TestRoundTrip(t *testing.T) {
	sizes := []int{0, 1, 5 << 20}

	testCases := []struct {
		Name  string
		Alg   compress.Algorithm
		Level compress.Level
	}{
		{Name: "zstd fastest", Alg: compress.Zstd, Level: compress.LevelFastest},
		{Name: "zstd default", Alg: compress.Zstd, Level: compress.LevelDefault},
		{Name: "zstd best", Alg: compress.Zstd, Level: compress.LevelBest},
		{Name: "gzip fastest", Alg: compress.Gzip, Level: compress.LevelFastest},
		{Name: "gzip default", Alg: compress.Gzip, Level: compress.LevelDefault},
		{Name: "gzip best", Alg: compress.Gzip, Level: compress.LevelBest},
	}

	for _, tc := range testCases {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s %d bytes", tc.Name, size), func(t *testing.T) {
				data := testData(size)

				compressed := compressStream(t, tc.Alg, data, 0, compress.WithLevel(tc.Level))

				require.Equal(t, data, decompressStream(t, tc.Alg, compressed))
			})
		}
	}
}

func TestWriterStreaming(t *testing.T) {
	data := testData(1 << 20)

	testCases := []struct {
		Name string
		Alg  compress.Algorithm
	}{
		{Name: "zstd", Alg: compress.Zstd},
		{Name: "gzip", Alg: compress.Gzip},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			single := compressStream(t, tc.Alg, data, 0)
			// 100-byte chunks of 1 MB is about ten thousand Write calls.
			chunked := compressStream(t, tc.Alg, data, 100)

			require.Equal(t, decompressStream(t, tc.Alg, single), decompressStream(t, tc.Alg, chunked))
			require.Equal(t, data, decompressStream(t, tc.Alg, chunked))
		})
	}
}

func TestWriterDoesNotCloseUnderlying(t *testing.T) {
	testCases := []struct {
		Name string
		Alg  compress.Algorithm
	}{
		{Name: "zstd", Alg: compress.Zstd},
		{Name: "gzip", Alg: compress.Gzip},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			dst := &closeCounter{}

			w, err := compress.NewWriter(dst, tc.Alg)
			require.NoError(t, err)

			_, err = w.Write(testData(1024))
			require.NoError(t, err)
			require.NoError(t, w.Close())

			require.Zero(t, dst.closes)
			require.Equal(t, testData(1024), decompressStream(t, tc.Alg, dst.Bytes()))
		})
	}
}

func TestMagicBytes(t *testing.T) {
	testCases := []struct {
		Name  string
		Alg   compress.Algorithm
		Magic []byte
	}{
		{Name: "zstd", Alg: compress.Zstd, Magic: []byte{0x28, 0xB5, 0x2F, 0xFD}},
		{Name: "gzip", Alg: compress.Gzip, Magic: []byte{0x1F, 0x8B}},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			compressed := compressStream(t, tc.Alg, []byte("hello"), 0)

			require.True(t, bytes.HasPrefix(compressed, tc.Magic), "got prefix % X", compressed[:len(tc.Magic)])
		})
	}
}

func TestBytesHelpers(t *testing.T) {
	data := testData(256 << 10)

	testCases := []struct {
		Name  string
		Alg   compress.Algorithm
		Level compress.Level
	}{
		{Name: "zstd default", Alg: compress.Zstd, Level: compress.LevelDefault},
		{Name: "zstd best", Alg: compress.Zstd, Level: compress.LevelBest},
		{Name: "gzip default", Alg: compress.Gzip, Level: compress.LevelDefault},
		{Name: "gzip best", Alg: compress.Gzip, Level: compress.LevelBest},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			compressed, err := compress.Compress(data, tc.Alg, compress.WithLevel(tc.Level))
			require.NoError(t, err)
			require.Less(t, len(compressed), len(data))

			decompressed, err := compress.Decompress(compressed, tc.Alg)
			require.NoError(t, err)
			require.Equal(t, data, decompressed)
		})
	}
}

func TestUnknownAlgorithm(t *testing.T) {
	alg := compress.Algorithm("lz4")

	testCases := []struct {
		Name string
		Call func() error
	}{
		{
			Name: "NewWriter",
			Call: func() error {
				_, err := compress.NewWriter(&bytes.Buffer{}, alg)
				return err
			},
		},
		{
			Name: "NewReader",
			Call: func() error {
				_, err := compress.NewReader(bytes.NewReader([]byte("data")), alg)
				return err
			},
		},
		{
			Name: "Compress",
			Call: func() error {
				_, err := compress.Compress([]byte("data"), alg)
				return err
			},
		},
		{
			Name: "Decompress",
			Call: func() error {
				_, err := compress.Decompress([]byte("data"), alg)
				return err
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			require.ErrorIs(t, tc.Call(), compress.ErrUnknownAlgorithm)
		})
	}

	require.Empty(t, alg.Extension())
	require.Empty(t, alg.ContentType())
}

func TestCorruptedInput(t *testing.T) {
	data := testData(256 << 10)

	testCases := []struct {
		Name    string
		Corrupt func(compressed []byte) []byte
	}{
		{
			Name:    "truncated in the middle",
			Corrupt: func(c []byte) []byte { return c[:len(c)/2] },
		},
		{
			Name:    "only first 4 bytes",
			Corrupt: func(c []byte) []byte { return c[:4] },
		},
		{
			Name:    "last byte cut",
			Corrupt: func(c []byte) []byte { return c[:len(c)-1] },
		},
		{
			Name:    "magic byte flipped",
			Corrupt: func(c []byte) []byte { return flipByte(c, 0) },
		},
		{
			Name:    "byte in the middle flipped",
			Corrupt: func(c []byte) []byte { return flipByte(c, len(c)/2) },
		},
		{
			Name: "random bytes",
			Corrupt: func(c []byte) []byte {
				garbage := make([]byte, len(c))
				rand.New(rand.NewSource(1)).Read(garbage)
				return garbage
			},
		},
	}

	for _, alg := range algorithms {
		compressed, err := compress.Compress(data, alg)
		require.NoError(t, err)

		for _, tc := range testCases {
			t.Run(fmt.Sprintf("%s %s", alg, tc.Name), func(t *testing.T) {
				corrupted := tc.Corrupt(bytes.Clone(compressed))

				var decompressErr error
				require.NotPanics(t, func() {
					_, decompressErr = compress.Decompress(corrupted, alg)
				})
				require.Error(t, decompressErr)
			})
		}
	}
}

func TestExtensionContentType(t *testing.T) {
	testCases := []struct {
		Name        string
		Alg         compress.Algorithm
		Extension   string
		ContentType string
	}{
		{Name: "zstd", Alg: compress.Zstd, Extension: ".zst", ContentType: "application/zstd"},
		{Name: "gzip", Alg: compress.Gzip, Extension: ".gz", ContentType: "application/gzip"},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			require.Equal(t, tc.Extension, tc.Alg.Extension())
			require.Equal(t, tc.ContentType, tc.Alg.ContentType())
		})
	}
}

// closeCounter is an io.WriteCloser that counts Close calls.
type closeCounter struct {
	bytes.Buffer
	closes int
}

func (c *closeCounter) Close() error {
	c.closes++
	return nil
}

// compressStream writes data through NewWriter in chunks of chunkSize bytes, or in one Write when chunkSize is 0.
func compressStream(t *testing.T, alg compress.Algorithm, data []byte, chunkSize int, opts ...compress.Option) []byte {
	t.Helper()

	var buf bytes.Buffer

	w, err := compress.NewWriter(&buf, alg, opts...)
	require.NoError(t, err)

	if chunkSize == 0 {
		chunkSize = len(data)
	}

	for start := 0; start < len(data); start += chunkSize {
		end := min(start+chunkSize, len(data))

		_, err = w.Write(data[start:end])
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())

	return buf.Bytes()
}

func decompressStream(t *testing.T, alg compress.Algorithm, compressed []byte) []byte {
	t.Helper()

	r, err := compress.NewReader(bytes.NewReader(compressed), alg)
	require.NoError(t, err)

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())

	return out
}

// testData returns size bytes of NDJSON rows shaped like the Tasnif snapshot, the same for the same size.
func testData(size int) []byte {
	rnd := rand.New(rand.NewSource(int64(size)))

	var buf bytes.Buffer
	buf.Grow(size + 256)

	for buf.Len() < size {
		fmt.Fprintf(&buf, `{"mxik_code":"%017d","barcode":"%013d","name":"Product %d","package_code":%d}`+"\n",
			rnd.Int63n(1e17), rnd.Int63n(1e13), rnd.Intn(1e6), rnd.Intn(1e6))
	}

	return buf.Bytes()[:size]
}

func flipByte(data []byte, i int) []byte {
	data[i] ^= 0xFF
	return data
}
