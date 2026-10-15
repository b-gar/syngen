package storage

import (
	"errors"
	"io"
	"os"
	"sync"
)

const blockSize = 8 * 1024 * 1024 // 8 MB cache block

type rangeFetchFunc func(offset, length int64, dst []byte) error

type BlockCacheReader struct {
	fetch      rangeFetchFunc
	size       int64
	mu         sync.Mutex
	blockStart int64
	blockEnd   int64
	blockData  []byte
}

func NewBlockCacheReader(size int64, fetch rangeFetchFunc) *BlockCacheReader {
	bufCap := min(max(size, 0), int64(blockSize))
	return &BlockCacheReader{
		fetch:      fetch,
		size:       size,
		blockData:  make([]byte, 0, bufCap),
		blockStart: -1,
		blockEnd:   -1,
	}
}

func (r *BlockCacheReader) Size() int64 {
	return r.size
}

func (r *BlockCacheReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blockData = nil
	return nil
}

func (r *BlockCacheReader) ReadAt(p []byte, off int64) (n int, err error) {
	if off < 0 {
		return 0, errors.New("storage: negative offset")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blockData == nil {
		return 0, os.ErrClosed
	}
	if off >= r.size {
		return 0, io.EOF
	}
	reqSize := min(int64(len(p)), r.size-off)
	bytesRead := 0
	for bytesRead < int(reqSize) {
		currentOff := off + int64(bytesRead)
		if r.blockStart != -1 && currentOff >= r.blockStart && currentOff < r.blockEnd {
			cacheOffset := currentOff - r.blockStart
			available := r.blockEnd - currentOff
			toRead := min(reqSize-int64(bytesRead), available)
			copy(p[bytesRead:], r.blockData[cacheOffset:cacheOffset+toRead])
			bytesRead += int(toRead)
			continue
		}
		if err = r.fetchBlock(currentOff); err != nil {
			return bytesRead, err
		}
	}
	if bytesRead < len(p) {
		return bytesRead, io.EOF
	}
	return bytesRead, nil
}

func (r *BlockCacheReader) fetchBlock(off int64) error {
	// Align the block fetch to the blockSize boundary for predictable caching
	r.blockStart = (off / blockSize) * blockSize
	r.blockEnd = min(r.blockStart+blockSize, r.size)
	length := r.blockEnd - r.blockStart
	r.blockData = r.blockData[:length]
	if err := r.fetch(r.blockStart, length, r.blockData); err != nil {
		r.blockStart = -1
		r.blockEnd = -1
		return err
	}
	return nil
}
