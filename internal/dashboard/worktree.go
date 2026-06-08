package dashboard

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"pi-mcp/internal/config"
)

// WorktreeActive reports whether a write job's worktree shows recent activity:
// the newest mtime under dir (excluding the .git and .pi bookkeeping trees,
// which churn independently of the agent's work) is within ±StaleThreshold of
// now. A small future mtime is plausible clock skew and still counts; a mtime
// far in the future is corrupt, not liveness. Empty/missing dir -> false.
func WorktreeActive(dir string, now time.Time) bool {
	if dir == "" {
		return false
	}
	var newest time.Time
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			if path != dir && (base == ".git" || base == ".pi") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) ||
			strings.Contains(path, string(filepath.Separator)+".pi"+string(filepath.Separator)) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if newest.IsZero() {
		return false
	}
	d := now.Sub(newest)
	if d < 0 {
		d = -d
	}
	return d <= config.StaleThreshold
}
