package generator

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/b-gar/syngen/internal/schema"
	"github.com/b-gar/syngen/internal/storage"
)

type SourceManager struct {
	RandomStreamers map[string]RowStreamer
	RandomTotals    map[string]int64
	Paths           map[string]string
	Sizes           map[string]int64
	Configs         map[string]schema.SourceConfig
	RequiredColumns map[string][]string
}

func NewSourceManager(tbl *schema.Table, outputPrefix string, partition int, format string, requiredColumns map[string][]string) (*SourceManager, error) {
	sm := &SourceManager{
		RandomStreamers: make(map[string]RowStreamer),
		RandomTotals:    make(map[string]int64),
		Paths:           make(map[string]string),
		Sizes:           make(map[string]int64),
		Configs:         tbl.Sources,
		RequiredColumns: requiredColumns,
	}
	for name, cfg := range tbl.Sources {
		path, err := resolveSourcePath(cfg, outputPrefix, partition, format)
		if err != nil {
			return nil, fmt.Errorf("source '%s': %w", name, err)
		}
		size, err := probeSourceFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to open source '%s' from '%s': %w", name, path, err)
		}
		sm.Paths[name] = path
		sm.Sizes[name] = size
	}
	for name, cfg := range tbl.Sources {
		if cfg.Selection == "random" {
			if err := sm.initRandomStreamer(name); err != nil {
				sm.Close()
				return nil, err
			}
		}
	}
	return sm, nil
}

func resolveSourcePath(cfg schema.SourceConfig, outputPrefix string, partition int, format string) (string, error) {
	if cfg.Table != "" {
		return fmt.Sprintf("%s%s_part_%d.%s", outputPrefix, cfg.Table, partition, format), nil
	}
	if cfg.Seed != "" {
		seedName := cfg.Seed
		if filepath.Ext(seedName) == "" {
			seedName = fmt.Sprintf("%s.%s", seedName, format)
		}
		return filepath.Join("seeds", seedName), nil
	}
	return "", fmt.Errorf("must specify either 'table' or 'seed'")
}

func probeSourceFile(path string) (int64, error) {
	return storage.StatFile(path)
}

func (sm *SourceManager) initRandomStreamer(name string) error {
	streamer, err := sm.NewStreamer(name, 0, true)
	if err != nil {
		return err
	}
	total, err := streamer.TotalRows()
	if err != nil {
		if closeErr := streamer.Close(); closeErr != nil {
			log.Printf("error closing random streamer '%s' after error: %v", name, closeErr)
		}
		return fmt.Errorf("failed to get total rows for random source '%s': %w", name, err)
	}
	if total == 0 {
		if closeErr := streamer.Close(); closeErr != nil {
			log.Printf("error closing random streamer '%s' after zero rows error: %v", name, closeErr)
		}
		return fmt.Errorf("random source '%s' has 0 rows", name)
	}
	sm.RandomStreamers[name] = streamer
	sm.RandomTotals[name] = total
	return nil
}

func (sm *SourceManager) Close() {
	for name, streamer := range sm.RandomStreamers {
		if err := streamer.Close(); err != nil {
			log.Printf("error closing random streamer '%s': %v", name, err)
		}
	}
}

func (sm *SourceManager) NewStreamer(name string, offset int64, isRandom bool) (RowStreamer, error) {
	path, ok := sm.Paths[name]
	if !ok {
		return nil, fmt.Errorf("source %s not found", name)
	}
	size := sm.Sizes[name]
	r, _, err := storage.OpenFileWithSize(path, size)
	if err != nil {
		return nil, err
	}
	var streamer RowStreamer
	if strings.HasSuffix(strings.ToLower(path), ".parquet") {
		if isRandom {
			streamer, err = NewMemoryParquetStreamer(r, size, sm.RequiredColumns[name])
		} else {
			streamer, err = NewParquetStreamer(r, size, sm.RequiredColumns[name])
		}
	} else {
		streamer, err = NewCSVStreamer(r, size, sm.RequiredColumns[name])
	}
	if err != nil {
		if closeErr := r.Close(); closeErr != nil {
			log.Printf("error closing source file %s: %v", path, closeErr)
		}
		return nil, err
	}
	// Seek to offset if required
	if offset > 0 {
		wrap := sm.Configs[name].Selection == "wrap"
		if err := streamer.SeekRow(offset, wrap); err != nil {
			if closeErr := streamer.Close(); closeErr != nil {
				log.Printf("error closing streamer for source %s: %v", name, closeErr)
			}
			return nil, err
		}
	}
	return streamer, nil
}
