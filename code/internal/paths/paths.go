// Package paths locates the repository root so every command works no matter which directory
// it is started from (repo root or code/). The root is the nearest ancestor containing the
// dataset/ directory next to AGENTS.md.
package paths

import (
	"os"
	"path/filepath"
)

// Root returns the repository root, falling back to the working directory.
func Root() string {
	wd, _ := os.Getwd()
	for d := wd; ; d = filepath.Dir(d) {
		if isDir(filepath.Join(d, "dataset")) && exists(filepath.Join(d, "AGENTS.md")) {
			return d
		}
		if filepath.Dir(d) == d {
			return wd
		}
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Join joins path elements onto the repository root.
func Join(elem ...string) string { return filepath.Join(append([]string{Root()}, elem...)...) }
