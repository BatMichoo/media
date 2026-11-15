package library

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// Apply makes the hard links, creating folders as needed. A destination
// that already exists is left alone and not reported as made. On the
// first other error it stops; the links made so far are still returned,
// so the caller can record or undo them.
func Apply(dataDir string, links []Link) (made []string, err error) {
	for _, l := range links {
		dst := filepath.Join(dataDir, filepath.FromSlash(l.To))
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return made, fmt.Errorf("make folder for %s: %w", l.To, err)
		}
		if err := os.Link(filepath.Join(dataDir, filepath.FromSlash(l.From)), dst); err != nil {
			return made, fmt.Errorf("link %s: %w", l.To, err)
		}
		made = append(made, l.To)
	}
	return made, nil
}

// stops are the folders Undo never removes.
var stops = map[string]bool{"library/Movies": true, "library/Shows": true, "library": true, ".": true, "": true}

// Undo removes these links (relative paths), then every folder they leave
// empty, up to library/Movies or library/Shows. Missing files are fine.
func Undo(dataDir string, paths []string) error {
	var errs []error
	for _, p := range paths {
		if err := os.Remove(filepath.Join(dataDir, filepath.FromSlash(p))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		for dir := path.Dir(p); !stops[dir]; dir = path.Dir(dir) {
			if err := os.Remove(filepath.Join(dataDir, filepath.FromSlash(dir))); err != nil {
				break // not empty (or already gone)
			}
		}
	}
	return errors.Join(errs...)
}

// CheckLinkable creates the folders the app needs, then makes and removes
// a test hard link from downloads/ into library/, which fails when the
// data folder is on a filesystem without hard links (exFAT, FAT, some
// NTFS) or the folders aren't writable.
func CheckLinkable(dataDir string) error {
	for _, d := range []string{"downloads", "library/Movies", "library/Shows"} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o755); err != nil {
			return fmt.Errorf("media folder: %w", err)
		}
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := ".media-linkcheck-" + hex.EncodeToString(b)
	src := filepath.Join(dataDir, "downloads", name)
	dst := filepath.Join(dataDir, "library", name)
	if err := os.WriteFile(src, nil, 0o644); err != nil {
		return fmt.Errorf("media folder: can't write to downloads/: %w", err)
	}
	defer os.Remove(src)
	if err := os.Link(src, dst); err != nil {
		return fmt.Errorf("media folder: can't hard-link from downloads/ into library/ (%w); "+
			"the folder must be on one filesystem with hard links, e.g. ext4, and writable", err)
	}
	return os.Remove(dst)
}
