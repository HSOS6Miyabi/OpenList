package wps

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

func TestCacheAndHash(t *testing.T) {
	oldConfig := conf.Conf
	conf.Conf = &conf.Config{TempDir: t.TempDir()}
	t.Cleanup(func() { conf.Conf = oldConfig })

	data := bytes.Repeat([]byte("WPS disk-staged upload"), 128*1024)
	tests := []struct {
		name         string
		data         []byte
		declaredSize int64
		cached       bool
	}{
		{name: "large nonseekable upload", data: data, declaredSize: int64(len(data))},
		{name: "unknown size", data: data[:1025], declaredSize: -1},
		{name: "zero declared size", data: data[:99], declaredSize: 0},
		{name: "empty upload"},
		{name: "already seekable stream", data: data[:2048], declaredSize: 2048, cached: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reader io.Reader = io.NopCloser(bytes.NewReader(tt.data))
			if tt.cached {
				r := bytes.NewReader(tt.data)
				if _, err := r.Seek(123, io.SeekStart); err != nil {
					t.Fatal(err)
				}
				reader = r
			}
			src := &stream.FileStream{
				Obj:    &model.Object{Name: "upload.bin", Size: tt.declaredSize},
				Reader: reader,
			}
			f, size, sha1Hex, sha256Hex, err := cacheAndHash(context.Background(), src)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			defer os.Remove(f.Name())

			if size != int64(len(tt.data)) {
				t.Fatalf("size = %d, want %d", size, len(tt.data))
			}
			sum1 := sha1.Sum(tt.data)
			sum256 := sha256.Sum256(tt.data)
			if sha1Hex != hex.EncodeToString(sum1[:]) || sha256Hex != hex.EncodeToString(sum256[:]) {
				t.Fatalf("hash mismatch: sha1=%s, sha256=%s", sha1Hex, sha256Hex)
			}
			got, err := io.ReadAll(f)
			if err != nil || !bytes.Equal(got, tt.data) {
				t.Fatalf("staged contents mismatch: read error %v", err)
			}
			if _, err := os.Stat(f.Name()); err != nil {
				t.Fatalf("temporary file is missing: %v", err)
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("source read failure")
}

func TestCacheAndHashRemovesTemporaryFileOnError(t *testing.T) {
	oldConfig := conf.Conf
	conf.Conf = &conf.Config{TempDir: t.TempDir()}
	t.Cleanup(func() { conf.Conf = oldConfig })

	tests := []struct {
		name string
		src  io.Reader
		size int64
		ctx  func() context.Context
	}{
		{
			name: "source failure",
			src:  failingReader{},
			size: -1,
			ctx:  context.Background,
		},
		{
			name: "short upload",
			src:  bytes.NewReader([]byte("short")),
			size: 100,
			ctx:  context.Background,
		},
		{
			name: "cancelled upload",
			src:  bytes.NewReader([]byte("cancelled")),
			size: -1,
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &stream.FileStream{Obj: &model.Object{Name: "upload.bin", Size: tt.size}, Reader: tt.src}
			f, _, _, _, err := cacheAndHash(tt.ctx(), src)
			if err == nil {
				_ = f.Close()
				_ = os.Remove(f.Name())
				t.Fatal("expected an error")
			}
			entries, err := os.ReadDir(conf.Conf.TempDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("left %d temporary files after failure", len(entries))
			}
		})
	}
}
