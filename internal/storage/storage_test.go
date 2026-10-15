package storage

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestIsRemotePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"s3://bucket/key", true},
		{"gs://bucket/key", true},
		{"output/file.parquet", false},
		{"/tmp/file.csv", false},
		{"http://example.com/file.csv", false},
	}
	for _, tt := range tests {
		if got := IsRemotePath(tt.path); got != tt.want {
			t.Errorf("IsRemotePath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestParseCloudPath(t *testing.T) {
	tests := []struct {
		path       string
		prefix     string
		wantBucket string
		wantKey    string
		wantErr    bool
	}{
		{"s3://my-bucket/dir/file.parquet", "s3://", "my-bucket", "dir/file.parquet", false},
		{"gs://my-gcs-bucket/file.csv", "gs://", "my-gcs-bucket", "file.csv", false},
		{"s3://my-bucket/", "s3://", "", "", true},
		{"gs://my-bucket", "gs://", "", "", true},
		{"gs:///key", "gs://", "", "", true},
	}
	for _, tt := range tests {
		b, k, err := parseCloudPath(tt.path, tt.prefix)
		if (err != nil) != tt.wantErr {
			t.Fatalf("parseCloudPath(%q, %q) err = %v, wantErr %v", tt.path, tt.prefix, err, tt.wantErr)
		}
		if b != tt.wantBucket || k != tt.wantKey {
			t.Errorf("parseCloudPath(%q, %q) = (%q, %q), want (%q, %q)", tt.path, tt.prefix, b, k, tt.wantBucket, tt.wantKey)
		}
	}
}

func TestBlockCacheReader(t *testing.T) {
	totalSize := int64(blockSize + 1024)
	sourceData := make([]byte, totalSize)
	for i := range sourceData {
		sourceData[i] = byte(i % 251)
	}
	fetchCalls := 0
	r := NewBlockCacheReader(totalSize, func(offset, length int64, dst []byte) error {
		fetchCalls++
		copy(dst, sourceData[offset:offset+length])
		return nil
	})
	if r.Size() != totalSize {
		t.Fatalf("Size() = %d, want %d", r.Size(), totalSize)
	}
	// Zero-length read should succeed without triggering a fetch
	if n, err := r.ReadAt(nil, 0); n != 0 || err != nil || fetchCalls != 0 {
		t.Fatalf("zero-length ReadAt failed: n=%d, err=%v, fetchCalls=%d", n, err, fetchCalls)
	}
	// First read within block 0
	buf := make([]byte, 100)
	n, err := r.ReadAt(buf, 50)
	if err != nil || n != 100 || !bytes.Equal(buf, sourceData[50:150]) {
		t.Fatalf("ReadAt block 0 failed: n=%d, err=%v", n, err)
	}
	if fetchCalls != 1 {
		t.Fatalf("expected 1 fetch call, got %d", fetchCalls)
	}
	// Second read within same cached block 0 should not trigger fetch
	n, err = r.ReadAt(buf, 500)
	if err != nil || n != 100 || !bytes.Equal(buf, sourceData[500:600]) {
		t.Fatalf("cached ReadAt failed: n=%d, err=%v", n, err)
	}
	if fetchCalls != 1 {
		t.Fatalf("expected cached read with 1 fetch call, got %d", fetchCalls)
	}
	// Read spanning across block 0 and block 1 boundary, up to EOF
	spanOffset := int64(blockSize - 50)
	spanBuf := make([]byte, 2000)
	n, err = r.ReadAt(spanBuf, spanOffset)
	wantN := int(totalSize - spanOffset)
	if !errors.Is(err, io.EOF) || n != wantN || !bytes.Equal(spanBuf[:n], sourceData[spanOffset:]) {
		t.Fatalf("spanning ReadAt failed: n=%d (want %d), err=%v", n, wantN, err)
	}
	// Negative offset and past-EOF offset
	if _, err := r.ReadAt(buf, -1); err == nil {
		t.Errorf("expected error for negative offset")
	}
	if n, err := r.ReadAt(buf, totalSize); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("expected (0, io.EOF) at totalSize, got (%d, %v)", n, err)
	}
	// Close and verify subsequent ReadAt (even at totalSize) returns os.ErrClosed
	if err := r.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	if _, err := r.ReadAt(buf, 0); !errors.Is(err, os.ErrClosed) {
		t.Errorf("expected os.ErrClosed after Close, got %v", err)
	}
	if _, err := r.ReadAt(buf, totalSize); !errors.Is(err, os.ErrClosed) {
		t.Errorf("expected os.ErrClosed at totalSize after Close, got %v", err)
	}
}

func TestBlockCacheReader_FetchErrorRecovery(t *testing.T) {
	buf := make([]byte, 10)
	shouldFail := true
	rErr := NewBlockCacheReader(100, func(_, length int64, dst []byte) error {
		if shouldFail {
			return errors.New("network error")
		}
		for i := range int(length) {
			dst[i] = 0xAB
		}
		return nil
	})
	defer func() {
		if closeErr := rErr.Close(); closeErr != nil {
			t.Errorf("Close() failed: %v", closeErr)
		}
	}()
	if _, err := rErr.ReadAt(buf, 0); err == nil {
		t.Fatalf("expected error from failing fetcher")
	}
	shouldFail = false
	if n, err := rErr.ReadAt(buf, 0); err != nil || n != 10 || buf[0] != 0xAB {
		t.Fatalf("expected recovery after transient fetch error, got n=%d, err=%v", n, err)
	}
}

func TestLocalFileOperationsAndInvalidCloudPaths(t *testing.T) {
	for _, badPath := range []string{"s3://bucket-only", "gs://bucket-only"} {
		if _, err := StatFile(badPath); err == nil {
			t.Errorf("StatFile(%q) expected error", badPath)
		}
		if _, _, err := OpenFile(badPath); err == nil {
			t.Errorf("OpenFile(%q) expected error", badPath)
		}
		if _, err := CreateFile(badPath); err == nil {
			t.Errorf("CreateFile(%q) expected error", badPath)
		}
	}
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "data.txt")
	w, err := CreateFile(filePath)
	if err != nil {
		t.Fatalf("CreateFile failed: %v", err)
	}
	payload := []byte("hello syngen storage")
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	size, err := StatFile(filePath)
	if err != nil || size != int64(len(payload)) {
		t.Fatalf("StatFile = (%d, %v), want (%d, nil)", size, err, len(payload))
	}
	r, openedSize, err := OpenFile(filePath)
	if err != nil || openedSize != size {
		t.Fatalf("OpenFile = (%d, %v), want (%d, nil)", openedSize, err, size)
	}
	got := make([]byte, size)
	if _, err := r.ReadAt(got, 0); err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close reader failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("read %q, want %q", got, payload)
	}
	// Verify local Abort removes the file
	abortPath := filepath.Join(tmpDir, "abort.txt")
	aw, err := CreateFile(abortPath)
	if err != nil {
		t.Fatalf("CreateFile abortPath failed: %v", err)
	}
	if aborter, ok := aw.(interface{ Abort(error) }); ok {
		aborter.Abort(errors.New("simulated failure"))
	}
	if _, err := os.Stat(abortPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected aborted file to be removed, stat err = %v", err)
	}
}

func BenchmarkBlockCacheReader_ReadAt(b *testing.B) {
	sourceData := make([]byte, blockSize)
	r := NewBlockCacheReader(int64(blockSize), func(_, length int64, dst []byte) error {
		copy(dst, sourceData[:length])
		return nil
	})
	defer func() {
		if err := r.Close(); err != nil {
			b.Fatal(err)
		}
	}()
	buf := make([]byte, 4096)
	b.ResetTimer()
	b.ReportAllocs()
	for i := range b.N {
		off := int64((i * 4096) % (blockSize - 4096))
		if _, err := r.ReadAt(buf, off); err != nil {
			b.Fatal(err)
		}
	}
}
