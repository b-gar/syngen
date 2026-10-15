package storage

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const blockSize = 8 * 1024 * 1024 // 8 MB cache block

type S3BlockCacheReader struct {
	client *s3.Client
	bucket string
	key    string
	size   int64

	mu         sync.Mutex
	blockStart int64
	blockEnd   int64
	blockData  []byte
}

func NewS3BlockCacheReader(ctx context.Context, client *s3.Client, bucket, key string) (*S3BlockCacheReader, error) {
	return NewS3BlockCacheReaderWithSize(ctx, client, bucket, key, -1)
}

func NewS3BlockCacheReaderWithSize(ctx context.Context, client *s3.Client, bucket, key string, knownSize int64) (*S3BlockCacheReader, error) {
	size := knownSize
	if size < 0 {
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return nil, err
		}
		size = 0
		if head.ContentLength != nil {
			size = *head.ContentLength
		}
	}
	bufCap := min(max(size, 0), int64(blockSize))
	return &S3BlockCacheReader{
		client:     client,
		bucket:     bucket,
		key:        key,
		size:       size,
		blockData:  make([]byte, 0, bufCap),
		blockStart: -1,
		blockEnd:   -1,
	}, nil
}

func (r *S3BlockCacheReader) Size() int64 {
	return r.size
}

func (r *S3BlockCacheReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blockData = nil // Free the cache
	return nil
}

func (r *S3BlockCacheReader) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= r.size {
		return 0, io.EOF
	}
	reqSize := min(int64(len(p)), r.size-off)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blockData == nil {
		return 0, os.ErrClosed
	}
	bytesRead := 0
	for bytesRead < int(reqSize) {
		currentOff := off + int64(bytesRead)
		// Check if currentOff is within the cached block
		if r.blockStart != -1 && currentOff >= r.blockStart && currentOff < r.blockEnd {
			// Serve from cache
			cacheOffset := currentOff - r.blockStart
			available := r.blockEnd - currentOff
			toRead := min(reqSize-int64(bytesRead), available)
			copy(p[bytesRead:], r.blockData[cacheOffset:cacheOffset+toRead])
			bytesRead += int(toRead)
			continue
		}
		// Not in cache, we need to fetch a new block
		err = r.fetchBlock(currentOff)
		if err != nil {
			return bytesRead, err
		}
	}
	if bytesRead < len(p) {
		return bytesRead, io.EOF
	}
	return bytesRead, nil
}

func (r *S3BlockCacheReader) fetchBlock(off int64) error {
	// Align the block fetch to the blockSize boundary for predictable caching
	r.blockStart = (off / blockSize) * blockSize
	r.blockEnd = min(r.blockStart+blockSize, r.size)
	rng := fmt.Sprintf("bytes=%d-%d", r.blockStart, r.blockEnd-1)
	resp, err := r.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(r.key),
		Range:  aws.String(rng),
	})
	if err != nil {
		r.blockStart = -1
		r.blockEnd = -1
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("error closing S3 response body: %v", closeErr)
		}
	}()
	r.blockData = r.blockData[:r.blockEnd-r.blockStart]
	if _, err = io.ReadFull(resp.Body, r.blockData); err != nil {
		r.blockStart = -1
		r.blockEnd = -1
		return err
	}
	return nil
}
