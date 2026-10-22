// Package compress streams data through zstd or gzip on top of github.com/klauspost/compress.
package compress

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

type Algorithm string

const (
	Zstd Algorithm = "zstd"
	Gzip Algorithm = "gzip"
)

var ErrUnknownAlgorithm = errors.New("compress: unknown algorithm")

type Level int

const (
	LevelDefault Level = iota
	LevelFastest
	LevelBest
)

// zstdWindowSize is the largest window RFC 8878 requires every decoder to support.
const zstdWindowSize = 8 << 20

type options struct {
	level Level
}

type Option func(*options)

func WithLevel(level Level) Option {
	return func(o *options) {
		o.level = level
	}
}

// Extension returns ".zst" for Zstd and ".gz" for Gzip.
func (a Algorithm) Extension() string {
	switch a {
	case Zstd:
		return ".zst"
	case Gzip:
		return ".gz"
	default:
		return ""
	}
}

// ContentType returns "application/zstd" for Zstd and "application/gzip" for Gzip.
func (a Algorithm) ContentType() string {
	switch a {
	case Zstd:
		return "application/zstd"
	case Gzip:
		return "application/gzip"
	default:
		return ""
	}
}

// NewWriter compresses everything written into w.
// Close flushes the end of the stream and does not close w.
func NewWriter(w io.Writer, alg Algorithm, opts ...Option) (io.WriteCloser, error) {
	o := options{level: LevelDefault}
	for _, opt := range opts {
		opt(&o)
	}

	switch alg {
	case Zstd:
		enc, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstdLevel(o.level)), zstd.WithWindowSize(zstdWindowSize))
		if err != nil {
			return nil, fmt.Errorf("compress: new zstd writer: %w", err)
		}

		return enc, nil
	case Gzip:
		gw, err := gzip.NewWriterLevel(w, gzipLevel(o.level))
		if err != nil {
			return nil, fmt.Errorf("compress: new gzip writer: %w", err)
		}

		return gw, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownAlgorithm, alg)
	}
}

// NewReader decompresses r. Close releases decoder resources and does not close r.
func NewReader(r io.Reader, alg Algorithm) (io.ReadCloser, error) {
	switch alg {
	case Zstd:
		dec, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
		if err != nil {
			if dec != nil {
				dec.Close()
			}

			return nil, fmt.Errorf("compress: new zstd reader: %w", err)
		}

		return zstdReader{Decoder: dec}, nil
	case Gzip:
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("compress: new gzip reader: %w", err)
		}

		return gr, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownAlgorithm, alg)
	}
}

func Compress(data []byte, alg Algorithm, opts ...Option) ([]byte, error) {
	var buf bytes.Buffer

	w, err := NewWriter(&buf, alg, opts...)
	if err != nil {
		return nil, err
	}

	if _, err = w.Write(data); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("compress: write: %w", err)
	}

	if err = w.Close(); err != nil {
		return nil, fmt.Errorf("compress: close writer: %w", err)
	}

	return buf.Bytes(), nil
}

func Decompress(data []byte, alg Algorithm) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(data), alg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()

	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("compress: read: %w", err)
	}

	return out, nil
}

// zstdReader adapts *zstd.Decoder, whose Close returns nothing, to io.ReadCloser.
// Embedding keeps Decoder.WriteTo, so io.Copy skips an extra buffer.
type zstdReader struct {
	*zstd.Decoder
}

func (r zstdReader) Close() error {
	r.Decoder.Close()
	return nil
}

func zstdLevel(level Level) zstd.EncoderLevel {
	switch level {
	case LevelFastest:
		return zstd.SpeedFastest
	case LevelBest:
		return zstd.SpeedBestCompression
	case LevelDefault:
		return zstd.SpeedDefault
	default:
		return zstd.SpeedDefault
	}
}

func gzipLevel(level Level) int {
	switch level {
	case LevelFastest:
		return gzip.BestSpeed
	case LevelBest:
		return gzip.BestCompression
	case LevelDefault:
		return gzip.DefaultCompression
	default:
		return gzip.DefaultCompression
	}
}
