package mcpserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateCWD(t *testing.T) {
	real := t.TempDir()
	// symlink pointing at real dir, to assert EvalSymlinks resolves it.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	file := filepath.Join(real, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tests := []struct {
		name    string
		in      string
		wantErr bool
		want    string // expected resolved path when wantErr==false
	}{
		{"empty", "", true, ""},
		{"relative", "relative/path", true, ""},
		{"dotdot", filepath.Join(real, "..", "x"), true, ""},
		{"missing", filepath.Join(real, "nope"), true, ""},
		{"file not dir", file, true, ""},
		{"ok abs dir", real, false, real},
		{"ok symlink resolved", link, false, real},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateCWD(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (got=%q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			wantResolved, _ := filepath.EvalSymlinks(tt.want)
			if got != wantResolved {
				t.Fatalf("got %q want %q", got, wantResolved)
			}
		})
	}
}

func TestValidateMode(t *testing.T) {
	if _, err := validateMode("read"); err != nil {
		t.Fatalf("read should be valid: %v", err)
	}
	if _, err := validateMode("write"); err != nil {
		t.Fatalf("write should be valid: %v", err)
	}
	if _, err := validateMode(""); err == nil {
		t.Fatalf("empty mode must be rejected")
	}
	if _, err := validateMode("admin"); err == nil {
		t.Fatalf("unknown mode must be rejected")
	}
}
