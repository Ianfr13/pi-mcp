package jobs

// persist_compat.go holds the legacy JSON persistence helpers that are
// still referenced by existing tests and reconcile.go.
// TODO(Task 3): delete this file once registry.go and all test files
// are migrated to use the SQLite store.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"pi-mcp/internal/model"
)

// persistedFile is the on-disk shape of the legacy JSON registry.
type persistedFile struct {
	Jobs []model.JobRecord `json:"jobs"`
}

// persist atomically writes records to path (tmp file + rename).
func persist(path string, records []model.JobRecord) error {
	if records == nil {
		records = []model.JobRecord{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(persistedFile{Jobs: records}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// loadPersisted reads records from path. A missing file is not an error; it
// yields an empty slice.
func loadPersisted(path string) ([]model.JobRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []model.JobRecord{}, nil
		}
		return nil, err
	}
	var pf persistedFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, err
	}
	if pf.Jobs == nil {
		return []model.JobRecord{}, nil
	}
	return pf.Jobs, nil
}
