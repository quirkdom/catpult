package room

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ValidateFilename enforces recipient safety (spec §11): no absolute
// paths, no parent traversal, no NUL, bounded length, and confinement
// under targetDir. It returns the cleaned absolute destination path.
func ValidateFilename(name, targetDir string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("invalid filename: empty")
	}
	if len(name) > 255 {
		return "", fmt.Errorf("invalid filename %q: exceeds 255 bytes", name)
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("invalid filename: contains NUL")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("invalid filename %q: absolute paths rejected", name)
	}
	if strings.HasPrefix(name, `\\`) {
		return "", fmt.Errorf("invalid filename %q: rejected", name)
	}
	// Reject any path element equal to ".." (covers "a/../../x" too,
	// after Clean below the prefix check is the final backstop).
	clean := filepath.Clean(name)
	if clean == "." {
		return "", fmt.Errorf("invalid filename %q: rejected", name)
	}
	if slices.Contains(strings.Split(filepath.ToSlash(clean), "/"), "..") {
		return "", fmt.Errorf("invalid filename %q: path traversal rejected", name)
	}
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return "", fmt.Errorf("resolving target dir: %w", err)
	}
	dest := filepath.Join(absTarget, clean)
	rel, err := filepath.Rel(absTarget, dest)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid filename %q: escapes target directory", name)
	}
	return dest, nil
}

// EnsureTargetDir creates the receive directory if missing (0700).
func EnsureTargetDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("empty target dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating target dir: %w", err)
	}
	return nil
}
