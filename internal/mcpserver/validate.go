package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pi-mcp/internal/model"
)

// validateMode enforces the REQUIRED read|write enum (no default).
func validateMode(m string) (model.JobMode, error) {
	switch m {
	case string(model.ModeRead):
		return model.ModeRead, nil
	case string(model.ModeWrite):
		return model.ModeWrite, nil
	case "":
		return "", fmt.Errorf("mode is required (read|write)")
	default:
		return "", fmt.Errorf("invalid mode %q (must be read|write)", m)
	}
}

// validateCWD enforces: non-empty, no ".." segment, absolute, exists, is a directory,
// and resolves symlinks. Returns the resolved absolute path. Never falls back to server cwd.
func validateCWD(in string) (string, error) {
	if strings.TrimSpace(in) == "" {
		return "", fmt.Errorf("cwd is required")
	}
	// Reject any ".." path segment explicitly (defense in depth).
	for _, seg := range strings.Split(filepath.ToSlash(in), "/") {
		if seg == ".." {
			return "", fmt.Errorf("cwd must not contain '..': %q", in)
		}
	}
	if !filepath.IsAbs(in) {
		return "", fmt.Errorf("cwd must be an absolute path: %q", in)
	}
	info, err := os.Stat(in)
	if err != nil {
		return "", fmt.Errorf("cwd does not exist: %q: %w", in, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd is not a directory: %q", in)
	}
	resolved, err := filepath.EvalSymlinks(in)
	if err != nil {
		return "", fmt.Errorf("cwd symlink resolution failed: %q: %w", in, err)
	}
	return resolved, nil
}
