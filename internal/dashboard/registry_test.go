package dashboard

import (
	"os"
	"path/filepath"
	"testing"

	"pi-mcp/internal/model"
)

func TestReadRegistry_Missing(t *testing.T) {
	recs, err := ReadRegistry(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should be no error, got %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("want 0 records, got %d", len(recs))
	}
}

func TestReadRegistry_Decodes(t *testing.T) {
	recs, err := ReadRegistry(filepath.Join("testdata", "registry.json"))
	if err != nil {
		t.Fatalf("ReadRegistry: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("want 4 records, got %d", len(recs))
	}
	if recs[0].JobID != "job-completed" || recs[0].Status != model.JobCompleted {
		t.Errorf("record[0] = %+v", recs[0])
	}
	if recs[3].Mode != model.ModeWrite || recs[3].Branch != "pi-mcp/job-queued" {
		t.Errorf("record[3] = %+v", recs[3])
	}
}

func TestReadRegistry_CorruptIsError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "reg.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegistry(p); err == nil {
		t.Errorf("corrupt registry should error")
	}
}
