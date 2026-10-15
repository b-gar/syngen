package storage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	gcs "cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	writeBufferSize = 4 * 1024 * 1024
	schemeS3        = "s3://"
	schemeGCS       = "gs://"
)

var (
	s3Client  *s3.Client
	s3Once    sync.Once
	s3InitErr error

	gcsClient  *gcs.Client
	gcsOnce    sync.Once
	gcsInitErr error
)

func getS3Client() (*s3.Client, error) {
	s3Once.Do(func() {
		cfg, err := config.LoadDefaultConfig(context.Background())
		if err != nil {
			s3InitErr = fmt.Errorf("unable to load AWS SDK config: %w", err)
			return
		}
		s3Client = s3.NewFromConfig(cfg)
	})
	return s3Client, s3InitErr
}

func getGCSClient() (*gcs.Client, error) {
	gcsOnce.Do(func() {
		client, err := gcs.NewClient(context.Background(), gcs.WithJSONReads())
		if err != nil {
			gcsInitErr = fmt.Errorf("unable to initialize GCS client: %w", err)
			return
		}
		gcsClient = client
	})
	return gcsClient, gcsInitErr
}

// IsRemotePath reports whether path targets a supported cloud storage backend (s3:// or gs://).
func IsRemotePath(path string) bool {
	return strings.HasPrefix(path, schemeS3) || strings.HasPrefix(path, schemeGCS)
}

func parseRemotePath(path string) (scheme, bucket, key string, err error) {
	switch {
	case strings.HasPrefix(path, schemeS3):
		scheme = schemeS3
	case strings.HasPrefix(path, schemeGCS):
		scheme = schemeGCS
	default:
		return "", "", "", nil
	}
	trimmed := strings.TrimPrefix(path, scheme)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("invalid cloud path %q: bucket and key are required", path)
	}
	return scheme, parts[0], parts[1], nil
}

// Reader represents an abstract file reader that supports io.ReaderAt.
type Reader interface {
	io.ReaderAt
	io.Closer
}

// StatFile returns the size of a local file, S3 object, or GCS object in bytes without allocating a read buffer.
func StatFile(path string) (int64, error) {
	scheme, bucket, key, err := parseRemotePath(path)
	if err != nil {
		return 0, err
	}
	switch scheme {
	case schemeS3:
		return statS3(bucket, key)
	case schemeGCS:
		return statGCS(bucket, key)
	default:
		info, err := os.Stat(path)
		if err != nil {
			return 0, err
		}
		return info.Size(), nil
	}
}

func statS3(bucket, key string) (int64, error) {
	client, err := getS3Client()
	if err != nil {
		return 0, err
	}
	head, err := client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, err
	}
	if head.ContentLength != nil {
		return *head.ContentLength, nil
	}
	return 0, nil
}

func statGCS(bucket, key string) (int64, error) {
	client, err := getGCSClient()
	if err != nil {
		return 0, err
	}
	attrs, err := client.Bucket(bucket).Object(key).Attrs(context.Background())
	if err != nil {
		return 0, err
	}
	return attrs.Size, nil
}

// OpenFile opens a local file, S3 object, or GCS object. It returns the Reader interface and the total size in bytes.
func OpenFile(path string) (Reader, int64, error) {
	return OpenFileWithSize(path, -1)
}

// OpenFileWithSize opens a local file, S3 object, or GCS object using a known size if non-negative to avoid redundant metadata calls.
func OpenFileWithSize(path string, knownSize int64) (Reader, int64, error) {
	scheme, bucket, key, err := parseRemotePath(path)
	if err != nil {
		return nil, 0, err
	}
	if scheme == "" {
		return openLocal(path, knownSize)
	}
	if knownSize < 0 {
		knownSize, err = StatFile(path)
		if err != nil {
			return nil, 0, err
		}
	}
	if scheme == schemeS3 {
		return openS3(bucket, key, knownSize)
	}
	return openGCS(bucket, key, knownSize)
}

func openS3(bucket, key string, size int64) (Reader, int64, error) {
	client, err := getS3Client()
	if err != nil {
		return nil, 0, err
	}
	r := NewBlockCacheReader(size, func(offset, length int64, dst []byte) error {
		rng := fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
		resp, err := client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Range:  aws.String(rng),
		})
		if err != nil {
			return err
		}
		return readFullAndClose(resp.Body, dst, "S3 response body")
	})
	return r, size, nil
}

func openGCS(bucket, key string, size int64) (Reader, int64, error) {
	client, err := getGCSClient()
	if err != nil {
		return nil, 0, err
	}
	obj := client.Bucket(bucket).Object(key)
	r := NewBlockCacheReader(size, func(offset, length int64, dst []byte) error {
		rc, err := obj.NewRangeReader(context.Background(), offset, length)
		if err != nil {
			return err
		}
		return readFullAndClose(rc, dst, "GCS range reader")
	})
	return r, size, nil
}

func readFullAndClose(rc io.ReadCloser, dst []byte, label string) error {
	_, err := io.ReadFull(rc, dst)
	if closeErr := rc.Close(); closeErr != nil {
		log.Printf("error closing %s: %v", label, closeErr)
	}
	return err
}

func openLocal(path string, knownSize int64) (Reader, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	if knownSize >= 0 {
		return f, knownSize, nil
	}
	info, err := f.Stat()
	if err != nil {
		if closeErr := f.Close(); closeErr != nil {
			log.Printf("error closing file %s: %v", path, closeErr)
		}
		return nil, 0, err
	}
	return f, info.Size(), nil
}

type bufferedWriteCloser struct {
	bw      *bufio.Writer
	closeFn func(flushErr error) error
	abortFn func(err error) error
	once    sync.Once
	err     error
}

func newBufferedWriteCloser(w io.Writer, closeFn func(flushErr error) error, abortFn func(err error) error) *bufferedWriteCloser {
	return &bufferedWriteCloser{
		bw:      bufio.NewWriterSize(w, writeBufferSize),
		closeFn: closeFn,
		abortFn: abortFn,
	}
}

func (b *bufferedWriteCloser) Write(p []byte) (int, error) {
	return b.bw.Write(p)
}

func (b *bufferedWriteCloser) Close() error {
	b.once.Do(func() {
		flushErr := b.bw.Flush()
		b.err = errors.Join(flushErr, b.closeFn(flushErr))
	})
	return b.err
}

func (b *bufferedWriteCloser) Abort(err error) {
	b.once.Do(func() {
		b.err = b.abortFn(err)
	})
}

// CreateFile creates a buffered local file, S3 object, or GCS object for writing.
func CreateFile(path string) (io.WriteCloser, error) {
	scheme, bucket, key, err := parseRemotePath(path)
	if err != nil {
		return nil, err
	}
	switch scheme {
	case schemeS3:
		return createS3(bucket, key)
	case schemeGCS:
		return createGCS(bucket, key)
	default:
		return createLocal(path)
	}
}

func createLocal(path string) (io.WriteCloser, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return newBufferedWriteCloser(f,
		func(_ error) error {
			return f.Close()
		},
		func(_ error) error {
			name := f.Name()
			if closeErr := f.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				log.Printf("error closing aborted file %s: %v", name, closeErr)
			}
			if rmErr := os.Remove(name); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				log.Printf("error removing aborted file %s: %v", name, rmErr)
			}
			return nil
		},
	), nil
}

func createS3(bucket, key string) (io.WriteCloser, error) {
	client, err := getS3Client()
	if err != nil {
		return nil, err
	}
	uploader := transfermanager.New(client)
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		_, upErr := uploader.UploadObject(context.Background(), &transfermanager.UploadObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   pr,
		})
		if upErr != nil {
			if closeErr := pr.CloseWithError(upErr); closeErr != nil {
				log.Printf("error closing pipe reader with error for s3 upload: %v", closeErr)
			}
		} else if closeErr := pr.Close(); closeErr != nil {
			log.Printf("error closing pipe reader for s3 upload: %v", closeErr)
		}
		errCh <- upErr
	}()
	return newBufferedWriteCloser(pw,
		func(flushErr error) error {
			var closeErr error
			if flushErr != nil {
				closeErr = pw.CloseWithError(flushErr)
			} else {
				closeErr = pw.Close()
			}
			return errors.Join(closeErr, <-errCh)
		},
		func(abortErr error) error {
			if closeErr := pw.CloseWithError(abortErr); closeErr != nil {
				log.Printf("error aborting pipe writer: %v", closeErr)
			}
			return <-errCh
		},
	), nil
}

func createGCS(bucket, key string) (io.WriteCloser, error) {
	client, err := getGCSClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	w := client.Bucket(bucket).Object(key).NewWriter(ctx)
	w.ChunkSize = blockSize
	return newBufferedWriteCloser(w,
		func(flushErr error) error {
			defer cancel(nil)
			if flushErr != nil {
				cancel(flushErr)
			}
			return w.Close()
		},
		func(abortErr error) error {
			// Canceling the context before Close prevents GCS from committing the object.
			cancel(abortErr)
			if closeErr := w.Close(); closeErr != nil && !errors.Is(closeErr, context.Canceled) && (abortErr == nil || !errors.Is(closeErr, abortErr)) {
				log.Printf("error closing aborted GCS writer: %v", closeErr)
			}
			return nil
		},
	), nil
}
