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

const writeBufferSize = 4 * 1024 * 1024

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
	return strings.HasPrefix(path, "s3://") || strings.HasPrefix(path, "gs://")
}

func parseCloudPath(path, prefix string) (bucket, key string, err error) {
	trimmed := strings.TrimPrefix(path, prefix)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid cloud path %q: bucket and key are required", path)
	}
	return parts[0], parts[1], nil
}

// Reader represents an abstract file reader that supports io.ReaderAt.
type Reader interface {
	io.ReaderAt
	io.Closer
}

// StatFile returns the size of a local file, S3 object, or GCS object in bytes without allocating a read buffer.
func StatFile(path string) (int64, error) {
	switch {
	case strings.HasPrefix(path, "s3://"):
		bucket, key, err := parseCloudPath(path, "s3://")
		if err != nil {
			return 0, err
		}
		return statS3(bucket, key)
	case strings.HasPrefix(path, "gs://"):
		bucket, key, err := parseCloudPath(path, "gs://")
		if err != nil {
			return 0, err
		}
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
	switch {
	case strings.HasPrefix(path, "s3://"):
		bucket, key, err := parseCloudPath(path, "s3://")
		if err != nil {
			return nil, 0, err
		}
		return openS3(bucket, key, knownSize)
	case strings.HasPrefix(path, "gs://"):
		bucket, key, err := parseCloudPath(path, "gs://")
		if err != nil {
			return nil, 0, err
		}
		return openGCS(bucket, key, knownSize)
	default:
		return openLocal(path, knownSize)
	}
}

func openS3(bucket, key string, knownSize int64) (Reader, int64, error) {
	client, err := getS3Client()
	if err != nil {
		return nil, 0, err
	}
	size := knownSize
	if size < 0 {
		size, err = statS3(bucket, key)
		if err != nil {
			return nil, 0, err
		}
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
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("error closing S3 response body: %v", closeErr)
			}
		}()
		_, err = io.ReadFull(resp.Body, dst)
		return err
	})
	return r, size, nil
}

func openGCS(bucket, key string, knownSize int64) (Reader, int64, error) {
	client, err := getGCSClient()
	if err != nil {
		return nil, 0, err
	}
	size := knownSize
	if size < 0 {
		size, err = statGCS(bucket, key)
		if err != nil {
			return nil, 0, err
		}
	}
	obj := client.Bucket(bucket).Object(key)
	r := NewBlockCacheReader(size, func(offset, length int64, dst []byte) error {
		rc, err := obj.NewRangeReader(context.Background(), offset, length)
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := rc.Close(); closeErr != nil {
				log.Printf("error closing GCS range reader: %v", closeErr)
			}
		}()
		_, err = io.ReadFull(rc, dst)
		return err
	})
	return r, size, nil
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

type S3WriteCloser struct {
	pw    *io.PipeWriter
	bw    *bufio.Writer
	errCh chan error
	once  sync.Once
	err   error
}

func (s *S3WriteCloser) Write(p []byte) (n int, err error) {
	return s.bw.Write(p)
}

func (s *S3WriteCloser) Close() error {
	s.once.Do(func() {
		flushErr := s.bw.Flush()
		var closeErr error
		if flushErr != nil {
			closeErr = s.pw.CloseWithError(flushErr)
		} else {
			closeErr = s.pw.Close()
		}
		s.err = errors.Join(flushErr, closeErr, <-s.errCh)
	})
	return s.err
}

func (s *S3WriteCloser) Abort(err error) {
	s.once.Do(func() {
		if closeErr := s.pw.CloseWithError(err); closeErr != nil {
			log.Printf("error aborting pipe writer: %v", closeErr)
		}
		s.err = <-s.errCh
	})
}

type GCSWriteCloser struct {
	w      *gcs.Writer
	bw     *bufio.Writer
	cancel context.CancelCauseFunc
	once   sync.Once
	err    error
}

func (g *GCSWriteCloser) Write(p []byte) (int, error) {
	return g.bw.Write(p)
}

func (g *GCSWriteCloser) Close() error {
	g.once.Do(func() {
		defer g.cancel(nil)
		flushErr := g.bw.Flush()
		if flushErr != nil {
			g.cancel(flushErr)
		}
		closeErr := g.w.Close()
		g.err = errors.Join(flushErr, closeErr)
	})
	return g.err
}

func (g *GCSWriteCloser) Abort(err error) {
	g.once.Do(func() {
		// Canceling the context before Close prevents GCS from committing the object.
		g.cancel(err)
		if closeErr := g.w.Close(); closeErr != nil && !errors.Is(closeErr, context.Canceled) && (err == nil || !errors.Is(closeErr, err)) {
			log.Printf("error closing aborted GCS writer: %v", closeErr)
		}
	})
}

type localWriteCloser struct {
	f    *os.File
	bw   *bufio.Writer
	once sync.Once
	err  error
}

func (l *localWriteCloser) Write(p []byte) (int, error) {
	return l.bw.Write(p)
}

func (l *localWriteCloser) Close() error {
	l.once.Do(func() {
		flushErr := l.bw.Flush()
		closeErr := l.f.Close()
		l.err = errors.Join(flushErr, closeErr)
	})
	return l.err
}

func (l *localWriteCloser) Abort(_ error) {
	l.once.Do(func() {
		name := l.f.Name()
		if closeErr := l.f.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			log.Printf("error closing aborted file %s: %v", name, closeErr)
		}
		if rmErr := os.Remove(name); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			log.Printf("error removing aborted file %s: %v", name, rmErr)
		}
	})
}

// CreateFile creates a buffered local file, S3 object, or GCS object for writing.
func CreateFile(path string) (io.WriteCloser, error) {
	switch {
	case strings.HasPrefix(path, "s3://"):
		bucket, key, err := parseCloudPath(path, "s3://")
		if err != nil {
			return nil, err
		}
		return createS3(bucket, key)
	case strings.HasPrefix(path, "gs://"):
		bucket, key, err := parseCloudPath(path, "gs://")
		if err != nil {
			return nil, err
		}
		return createGCS(bucket, key)
	default:
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		return &localWriteCloser{
			f:  f,
			bw: bufio.NewWriterSize(f, writeBufferSize),
		}, nil
	}
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
	return &S3WriteCloser{
		pw:    pw,
		bw:    bufio.NewWriterSize(pw, writeBufferSize),
		errCh: errCh,
	}, nil
}

func createGCS(bucket, key string) (io.WriteCloser, error) {
	client, err := getGCSClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	w := client.Bucket(bucket).Object(key).NewWriter(ctx)
	w.ChunkSize = blockSize
	return &GCSWriteCloser{
		w:      w,
		bw:     bufio.NewWriterSize(w, writeBufferSize),
		cancel: cancel,
	}, nil
}
