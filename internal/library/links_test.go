package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(rel), 0o644))
}

func exists(dir, rel string) bool {
	_, err := os.Lstat(filepath.Join(dir, rel))
	return err == nil
}

func TestApplyAndUndo(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, CheckLinkable(dir))
	write(t, dir, "downloads/D/Dune.mkv")
	write(t, dir, "downloads/D/Dune.en.srt")
	links := []Link{
		{"downloads/D/Dune.mkv", "library/Movies/Dune (2021)/Dune (2021).mkv"},
		{"downloads/D/Dune.en.srt", "library/Movies/Dune (2021)/Dune (2021).en.srt"},
	}
	made, err := Apply(dir, links)
	require.NoError(t, err)
	assert.Equal(t, []string{links[0].To, links[1].To}, made)

	a, err := os.Stat(filepath.Join(dir, links[0].From))
	require.NoError(t, err)
	b, err := os.Stat(filepath.Join(dir, links[0].To))
	require.NoError(t, err)
	assert.True(t, os.SameFile(a, b), "hard link, not a copy")

	require.NoError(t, Undo(dir, made))
	assert.False(t, exists(dir, "library/Movies/Dune (2021)"), "empty movie folder removed")
	assert.True(t, exists(dir, "library/Movies"))
	assert.True(t, exists(dir, "downloads/D/Dune.mkv"), "download untouched")
}

func TestApplyExistingSkipped(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "downloads/a.mkv")
	write(t, dir, "library/Shows/S/a.mkv") // someone else's file
	made, err := Apply(dir, []Link{{"downloads/a.mkv", "library/Shows/S/a.mkv"}})
	require.NoError(t, err)
	assert.Empty(t, made)
	got, _ := os.ReadFile(filepath.Join(dir, "library/Shows/S/a.mkv"))
	assert.Equal(t, "library/Shows/S/a.mkv", string(got), "existing file untouched")
}

func TestApplyStopsOnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "downloads/a.mkv")
	made, err := Apply(dir, []Link{
		{"downloads/a.mkv", "library/Movies/A/A.mkv"},
		{"downloads/missing.mkv", "library/Movies/A/B.mkv"},
		{"downloads/a.mkv", "library/Movies/A/C.mkv"},
	})
	assert.Error(t, err)
	assert.Equal(t, []string{"library/Movies/A/A.mkv"}, made, "links made before the error are returned")
	assert.False(t, exists(dir, "library/Movies/A/C.mkv"))
}

func TestUndoKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "downloads/one/S01E01.mkv")
	write(t, dir, "downloads/two/S01E02.mkv")
	m1, err := Apply(dir, []Link{{"downloads/one/S01E01.mkv", "library/Shows/S/one/S01E01.mkv"}})
	require.NoError(t, err)
	m2, err := Apply(dir, []Link{{"downloads/two/S01E02.mkv", "library/Shows/S/two/S01E02.mkv"}})
	require.NoError(t, err)
	require.NoError(t, Undo(dir, m2))
	assert.True(t, exists(dir, m1[0]))
	assert.True(t, exists(dir, "library/Shows/S"))
	assert.False(t, exists(dir, "library/Shows/S/two"))
}

func TestUndoMissingIsFine(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, CheckLinkable(dir))
	assert.NoError(t, Undo(dir, []string{"library/Movies/Gone/Gone.mkv"}))
}

func TestCheckLinkable(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, CheckLinkable(dir))
	for _, d := range []string{"downloads", "library/Movies", "library/Shows"} {
		assert.True(t, exists(dir, d), d)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "library"))
	assert.Len(t, entries, 2, "test files cleaned up")
}

func TestCheckLinkableFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	dir := t.TempDir()
	require.NoError(t, CheckLinkable(dir))
	lib := filepath.Join(dir, "library")
	require.NoError(t, os.Chmod(lib, 0o555))
	t.Cleanup(func() { _ = os.Chmod(lib, 0o755) })
	err := CheckLinkable(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filesystem")
}
