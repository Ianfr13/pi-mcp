// Package dashboard implements pi-dashboard: a read-only realtime viewer of
// pi-mcp workflows. It reads the registry.json job index and the per-job run
// files, derives a view-model, and serves it over HTTP + SSE.
package dashboard

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"pi-mcp/internal/model"
)

// persistedRegistry mirrors the on-disk shape written by internal/jobs.
type persistedRegistry struct {
	Jobs []model.JobRecord `json:"jobs"`
}

// ReadRegistry decodes the registry file into job records. A missing file is not
// an error (the pi-mcp server may not have run yet) and yields an empty slice. A
// present-but-corrupt file IS an error (callers keep their last good state). The
// pi-mcp server writes the registry via atomic rename, so a successful read is
// always a complete file.
func ReadRegistry(path string) ([]model.JobRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []model.JobRecord{}, nil
		}
		return nil, err
	}
	var pr persistedRegistry
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, err
	}
	if pr.Jobs == nil {
		return []model.JobRecord{}, nil
	}
	return pr.Jobs, nil
}
