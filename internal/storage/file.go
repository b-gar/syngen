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

func parseS3Path(path string) (bucket, key string, err error) {
	trimmed := strings.TrimPrefix(path, "s3://")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid S3 path %q: bucket and key are required", path)
	}
	return parts[0], parts[1], nil
}

// Reader represents an abstract file reader that supports io.ReaderAt
type Reader interface {
	io.ReaderAt
	io.Closer
}

// StatFile returns the size of a local file or S3 object in bytes without allocating a read buffer.
func StatFile(path string) (int64, error) {
	if strings.HasPrefix(path, "s3://") {
		bucket, key, err := parseS3Path(path)
		if err != nil {
			return 0, err
		}
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
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// OpenFile opens a local file or an S3 object. It returns the Reader interface and the total size in bytes.
func OpenFile(path string) (Reader, int64, error) {
	return OpenFileWithSize(path, -1)
}

// OpenFileWithSize opens a local file or an S3 object using a known size if non-negative to avoid redundant HeadObject calls.
func OpenFileWithSize(path string, knownSize int64) (Reader, int64, error) {
	if strings.HasPrefix(path, "s3://") {
		bucket, key, err := parseS3Path(path)
		if err != nil {
			return nil, 0, err
		}
		client, err := getS3Client()
		if err != nil {
			return nil, 0, err
		}
		r, err := NewS3BlockCacheReaderWithSize(context.Background(), client, bucket, key, knownSize)
		if err != nil {
			return nil, 0, err
		}
		return r, r.Size(), nil
	}
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
		closeErr := s.pw.Close()
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

// CreateFile creates a buffered local file or an S3 object for writing.
func CreateFile(path string) (io.WriteCloser, error) {
	if strings.HasPrefix(path, "s3://") {
		bucket, key, err := parseS3Path(path)
		if err != nil {
			return nil, err
		}
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
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &localWriteCloser{
		f:  f,
		bw: bufio.NewWriterSize(f, writeBufferSize),
	}, nil
}
