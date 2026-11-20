package downloads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BatMichoo/media/internal/db"
	"github.com/BatMichoo/media/internal/db/dbtest"
	"github.com/BatMichoo/media/internal/library"
	"github.com/BatMichoo/media/internal/model"
	"github.com/BatMichoo/media/internal/transmission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const GB = int64(1) << 30

// fakeTorrents is Transmission in memory.
type fakeTorrents struct {
	mu        sync.Mutex
	torrents  map[string]transmission.Torrent
	adds      int
	removed   map[string]bool // hash -> deleteData
	stopped   []string
	started   []string
	seq       []string
	seqOff    []string
	startNow  []string
	portCalls int
	portOpen  bool
	portErr   error
	wantSet   []string // "hash +0,1 -2" per SetWanted call
	placed    map[string]string
	nextHash  string
	nextFiles []transmission.File // the next added torrent's files (a .torrent file's)
	getErr    error
	gate      chan struct{} // when set, Get waits for it (to hold a poll mid-round)
	inGet     chan struct{}
}

func newFakeTorrents() *fakeTorrents {
	return &fakeTorrents{torrents: map[string]transmission.Torrent{}, removed: map[string]bool{}, placed: map[string]string{}, nextHash: "h1"}
}

func (f *fakeTorrents) Add(_ context.Context, magnet string, file []byte, dir string) (string, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds++
	h := f.nextHash
	_, dup := f.torrents[h]
	if !dup {
		f.torrents[h] = transmission.Torrent{Hash: h, Name: "Torrent " + h, Files: f.nextFiles}
	}
	return h, "Torrent " + h, dup, nil
}

func (f *fakeTorrents) Get(_ context.Context, hashes []string) (map[string]transmission.Torrent, error) {
	if f.gate != nil {
		gate := f.gate
		f.gate = nil
		f.inGet <- struct{}{}
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := map[string]transmission.Torrent{}
	for _, h := range hashes {
		if t, ok := f.torrents[h]; ok {
			out[h] = t
		}
	}
	return out, nil
}

func (f *fakeTorrents) Remove(_ context.Context, hash string, deleteData bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed[hash] = deleteData
	delete(f.torrents, hash)
	return nil
}

func (f *fakeTorrents) Place(_ context.Context, hash, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.placed[hash] = dir
	return nil
}

func (f *fakeTorrents) Stop(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, hash)
	t := f.torrents[hash]
	t.Stopped = true
	f.torrents[hash] = t
	return nil
}

func (f *fakeTorrents) Start(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, hash)
	t := f.torrents[hash]
	t.Stopped = false
	f.torrents[hash] = t
	return nil
}

func (f *fakeTorrents) SetSequential(_ context.Context, hash string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.seq = append(f.seq, hash)
	} else {
		f.seqOff = append(f.seqOff, hash)
	}
	t := f.torrents[hash]
	t.Sequential = on
	f.torrents[hash] = t
	return nil
}

func (f *fakeTorrents) SetWanted(_ context.Context, hash string, wanted, unwanted []int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(wanted)+len(unwanted) == 0 {
		return nil
	}
	f.wantSet = append(f.wantSet, fmt.Sprintf("%s +%v -%v", hash, wanted, unwanted))
	t := f.torrents[hash]
	files := append([]transmission.File(nil), t.Files...)
	for _, i := range wanted {
		files[i].Wanted = true
	}
	for _, i := range unwanted {
		files[i].Wanted = false
	}
	t.Files = files
	f.torrents[hash] = t
	return nil
}

func (f *fakeTorrents) StartNow(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startNow = append(f.startNow, hash)
	t := f.torrents[hash]
	t.Queued = false
	f.torrents[hash] = t
	return nil
}

func (f *fakeTorrents) PortOpen(context.Context) (bool, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.portCalls++
	return f.portOpen, 51413, f.portErr
}

func (f *fakeTorrents) set(t transmission.Torrent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.torrents[t.Hash] = t
}

type fakeLibrary struct{ refreshes int }

func (l *fakeLibrary) Refresh(context.Context) error { l.refreshes++; return nil }

type env struct {
	s   *Service
	t   *fakeTorrents
	lib *fakeLibrary
	dir string
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, library.CheckLinkable(dir))
	e := &env{t: newFakeTorrents(), lib: &fakeLibrary{}, dir: dir, now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	e.s = New(dbtest.New(t), e.t, e.lib, dir)
	e.s.Now = func() time.Time { return e.now }
	return e
}

var alice = model.User{ID: "6f1c2d3e-0000-4000-8000-000000000001", Name: "Alice"}

// file writes a downloaded file in the torrent's own folder.
func (e *env) file(t *testing.T, hash, rel string) transmission.File {
	t.Helper()
	p := filepath.Join(e.dir, "downloads", hash, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(rel), 0o644))
	return transmission.File{Name: rel, Length: 2 * GB}
}

func (e *env) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(e.dir, rel))
	return err == nil
}

func (e *env) addMovie(t *testing.T, hash, title string, year int) {
	t.Helper()
	e.t.nextHash = hash
	_, _, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?xt=urn:btih:" + hash, Kind: "movie", Title: title, Year: year})
	require.NoError(t, err)
}

func (e *env) finish(t *testing.T, hash string, files ...transmission.File) {
	t.Helper()
	e.t.set(transmission.Torrent{Hash: hash, Name: "Torrent " + hash, PercentDone: 1, Files: files, DoneDate: e.now})
}

func (e *env) get(t *testing.T, hash string) Download {
	t.Helper()
	list, err := e.s.List(context.Background())
	require.NoError(t, err)
	for _, d := range list {
		if d.Hash == hash {
			return d
		}
	}
	t.Fatalf("no download %s", hash)
	return Download{}
}

func TestAddRecords(t *testing.T) {
	e := newEnv(t)
	d, existing, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?xt=1", Kind: "movie", Title: " Dune ", Year: 2021})
	require.NoError(t, err)
	assert.False(t, existing)
	assert.Equal(t, "h1", d.Hash)
	got := e.get(t, "h1")
	assert.Equal(t, "movie", got.Kind)
	assert.Equal(t, "Dune", got.Title)
	assert.Equal(t, 2021, got.Year)
	assert.Equal(t, "Alice", got.AddedByName)
	assert.Equal(t, "downloading", got.State)
	assert.Equal(t, "Torrent h1", got.Name)
}

func TestAddDuplicateReturnsExisting(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	d, existing, err := e.s.Add(context.Background(), alice, AddInput{File: []byte("torrent"), Kind: "movie", Title: "Other", Year: 2000})
	require.NoError(t, err)
	assert.True(t, existing)
	assert.Equal(t, "Dune", d.Title)
	list, err := e.s.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestAddValidation(t *testing.T) {
	e := newEnv(t)
	for name, in := range map[string]AddInput{
		"nothing":       {Kind: "movie", Title: "X", Year: 2000},
		"both":          {Magnet: "magnet:?x", File: []byte("x"), Kind: "movie", Title: "X", Year: 2000},
		"not a magnet":  {Magnet: "http://x", Kind: "movie", Title: "X", Year: 2000},
		"kind":          {Magnet: "magnet:?x", Kind: "music", Title: "X", Year: 2000},
		"empty title":   {Magnet: "magnet:?x", Kind: "movie", Title: "???", Year: 2000},
		"movie no year": {Magnet: "magnet:?x", Kind: "movie", Title: "X"},
		"year too old":  {Magnet: "magnet:?x", Kind: "series", Title: "X", Year: 1700},
		"year too new":  {Magnet: "magnet:?x", Kind: "movie", Title: "X", Year: 2200},
	} {
		_, _, err := e.s.Add(context.Background(), alice, in)
		assert.ErrorIs(t, err, ErrInvalid, name)
		var ie *InvalidError
		assert.True(t, errors.As(err, &ie), name)
	}
	assert.Equal(t, 0, e.t.adds, "nothing sent to Transmission")
	// A series may leave the year out.
	_, _, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?x", Kind: "series", Title: "S"})
	assert.NoError(t, err)
}

func TestPollFilesMovie(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Equal(t, "downloading", e.get(t, "h1").State)
	assert.Equal(t, 0, e.lib.refreshes)

	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"), e.file(t, "h1", "D/Dune.en.srt"))
	require.NoError(t, e.s.Poll(context.Background()))
	d := e.get(t, "h1")
	assert.Equal(t, "filed", d.State)
	assert.True(t, e.exists("library/Movies/Dune (2021)/Dune (2021).mkv"))
	assert.True(t, e.exists("library/Movies/Dune (2021)/Dune (2021).en.srt"))
	links, err := e.s.store.Links(context.Background(), "h1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"library/Movies/Dune (2021)/Dune (2021).mkv", "library/Movies/Dune (2021)/Dune (2021).en.srt"}, links)
	assert.Equal(t, 1, e.lib.refreshes)

	// Filed stays filed; no more refreshes.
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Equal(t, 1, e.lib.refreshes)
}

func TestPollRemovedExternally(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	delete(e.t.torrents, "h1")
	require.NoError(t, e.s.Poll(context.Background()))
	d := e.get(t, "h1")
	assert.Equal(t, "failed", d.State)
	assert.Equal(t, "removed from Transmission", d.Reason)
}

func TestPollTransmissionError(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", Error: "No data found", LocalError: true})
	require.NoError(t, e.s.Poll(context.Background()))
	d := e.get(t, "h1")
	assert.Equal(t, "downloading", d.State)
	assert.Equal(t, "No data found", d.Reason)
}

func TestPollNoVideo(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	e.finish(t, "h1", e.file(t, "h1", "D/readme.txt"))
	require.NoError(t, e.s.Poll(context.Background()))
	d := e.get(t, "h1")
	assert.Equal(t, "failed", d.State)
	assert.Equal(t, "no video files", d.Reason)
	assert.Equal(t, 0, e.lib.refreshes)
}

func TestPollLinkFailure(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	// The listed file isn't on disk: linking fails.
	e.finish(t, "h1", transmission.File{Name: "D/Dune.mkv", Length: 2 * GB})
	require.NoError(t, e.s.Poll(context.Background()))
	d := e.get(t, "h1")
	assert.Equal(t, "failed", d.State)
	assert.Contains(t, d.Reason, "link")
}

func TestPollResumesAfterCrash(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	f := e.file(t, "h1", "D/Dune.mkv")
	// An earlier round linked the movie, then the app stopped before
	// recording it.
	_, err := library.Apply(e.dir, []library.Link{{From: "downloads/h1/D/Dune.mkv", To: "library/Movies/Dune (2021)/Dune (2021).mkv"}})
	require.NoError(t, err)
	e.finish(t, "h1", f)
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Equal(t, "filed", e.get(t, "h1").State)
	links, err := e.s.store.Links(context.Background(), "h1")
	require.NoError(t, err)
	assert.Equal(t, []string{"library/Movies/Dune (2021)/Dune (2021).mkv"}, links)
}

func TestPollTransmissionDown(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.getErr = errors.New("connection refused")
	assert.Error(t, e.s.Poll(context.Background()))
	list, err := e.s.List(context.Background())
	assert.Error(t, err, "list says Transmission is unreachable")
	require.Len(t, list, 1, "but still lists what was added")
	assert.Equal(t, "Dune", list[0].Title)
}

func TestRemoveDeleteFiles(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(context.Background()))
	require.NoError(t, e.s.Remove(context.Background(), "h1", true))
	assert.Equal(t, map[string]bool{"h1": true}, e.t.removed)
	assert.False(t, e.exists("library/Movies/Dune (2021)"))
	list, err := e.s.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.Equal(t, 2, e.lib.refreshes)
}

func TestRemoveKeepFiles(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(context.Background()))
	require.NoError(t, e.s.Remove(context.Background(), "h1", false))
	assert.Equal(t, map[string]bool{"h1": false}, e.t.removed)
	assert.True(t, e.exists("library/Movies/Dune (2021)/Dune (2021).mkv"))
	list, _ := e.s.List(context.Background())
	assert.Empty(t, list)
}

func TestRemoveUnknown(t *testing.T) {
	e := newEnv(t)
	assert.ErrorIs(t, e.s.Remove(context.Background(), "nope", true), ErrNotFound)
}

func TestSameReleaseTwice(t *testing.T) {
	// Two torrents of the same release (say a magnet and a .torrent): each
	// downloads into its own folder, so removing one with its files never
	// takes the other's.
	e := newEnv(t)
	for _, h := range []string{"s1", "s2"} {
		e.t.nextHash = h
		_, _, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?x" + h, Kind: "series", Title: "Severance"})
		require.NoError(t, err)
		e.finish(t, h, e.file(t, h, "S/Severance.S01E01.mkv"))
		require.NoError(t, e.s.Poll(context.Background())) // s1 is filed before s2 arrives
	}
	assert.Equal(t, "downloads/s1", e.t.placed["s1"][len(e.dir)+1:])
	assert.True(t, e.exists("library/Shows/Severance/S/Severance.S01E01.mkv"))
	l2, _ := e.s.store.Links(context.Background(), "s2")
	assert.Empty(t, l2, "s1's file is already there")
	require.NoError(t, e.s.Remove(context.Background(), "s2", true))
	assert.True(t, e.exists("library/Shows/Severance/S/Severance.S01E01.mkv"), "s1's library file stays")
	assert.True(t, e.exists("downloads/s1/S/Severance.S01E01.mkv"), "s1's data stays")
}

func TestAddPlacesInOwnFolder(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	assert.Equal(t, filepath.Join(e.dir, "downloads", "h1"), e.t.placed["h1"])
	// Already in Transmission: left where it is.
	e.t.nextHash = "h1"
	_, _, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?again", Kind: "movie", Title: "Dune", Year: 2021})
	require.NoError(t, err)
	assert.Len(t, e.t.placed, 1)
}

func TestPollFilesDespiteTrackerError(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	tor := transmission.Torrent{Hash: "h1", PercentDone: 1, Error: "unregistered torrent", DoneDate: e.now,
		Files: []transmission.File{e.file(t, "h1", "D/Dune.mkv")}}
	e.t.set(tor)
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Equal(t, "filed", e.get(t, "h1").State)
}

func TestEditRetriesFailed(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	// Listed but not on disk yet: linking fails.
	e.finish(t, "h1", transmission.File{Name: "D/Dune.mkv", Length: 2 * GB})
	require.NoError(t, e.s.Poll(context.Background()))
	require.Equal(t, "failed", e.get(t, "h1").State)
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv")) // the cause is fixed
	require.NoError(t, e.s.Edit(context.Background(), "h1", "movie", "Dune", 2021))
	assert.Equal(t, "filed", e.get(t, "h1").State)
	assert.True(t, e.exists("library/Movies/Dune (2021)/Dune (2021).mkv"))
}

func TestRemoveNeverDeletesFileItDidntMake(t *testing.T) {
	e := newEnv(t)
	// Something already sits where the second download would go.
	e.t.nextHash = "a"
	_, _, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?a", Kind: "series", Title: "S"})
	require.NoError(t, err)
	e.finish(t, "a", e.file(t, "a", "x/E01.mkv"))
	require.NoError(t, e.s.Poll(context.Background()))
	e.t.nextHash = "b"
	_, _, err = e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?b", Kind: "series", Title: "S"})
	require.NoError(t, err)
	// b's file has the same path below the show folder as a's.
	e.finish(t, "b", e.file(t, "b", "x/E01.mkv")) // another file, same name
	require.NoError(t, e.s.Poll(context.Background()))
	links, _ := e.s.store.Links(context.Background(), "b")
	assert.Empty(t, links, "the existing file isn't b's")
	require.NoError(t, e.s.Remove(context.Background(), "b", true))
	assert.True(t, e.exists("library/Shows/S/x/E01.mkv"), "a's file survives b's removal")
}

func TestEditRefiles(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2020)
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(context.Background()))
	require.True(t, e.exists("library/Movies/Dune (2020)/Dune (2020).mkv"))
	adds := e.t.adds

	require.NoError(t, e.s.Edit(context.Background(), "h1", "movie", "Dune", 2021))
	assert.False(t, e.exists("library/Movies/Dune (2020)"))
	assert.True(t, e.exists("library/Movies/Dune (2021)/Dune (2021).mkv"))
	d := e.get(t, "h1")
	assert.Equal(t, "filed", d.State)
	assert.Equal(t, 2021, d.Year)
	assert.Equal(t, adds, e.t.adds, "nothing downloaded again")
	links, _ := e.s.store.Links(context.Background(), "h1")
	assert.Equal(t, []string{"library/Movies/Dune (2021)/Dune (2021).mkv"}, links)
}

func TestEditWhileDownloading(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2020)
	require.NoError(t, e.s.Edit(context.Background(), "h1", "series", "Dune Prophecy", 0))
	d := e.get(t, "h1")
	assert.Equal(t, "series", d.Kind)
	assert.Equal(t, "Dune Prophecy", d.Title)
	assert.Equal(t, "downloading", d.State)
	assert.ErrorIs(t, e.s.Edit(context.Background(), "h1", "movie", "???", 2000), ErrInvalid)
	assert.ErrorIs(t, e.s.Edit(context.Background(), "nope", "movie", "X", 2000), ErrNotFound)
}

func TestPollStopsAfter72h(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "old", "Old", 2000)
	e.addMovie(t, "young", "Young", 2000)
	e.addMovie(t, "done", "Done", 2000)
	e.t.set(transmission.Torrent{Hash: "old", PercentDone: 1, Files: []transmission.File{e.file(t, "old", "o/o.mkv")}, DoneDate: e.now.Add(-72*time.Hour - time.Minute)})
	e.t.set(transmission.Torrent{Hash: "young", PercentDone: 1, Files: []transmission.File{e.file(t, "young", "y/y.mkv")}, DoneDate: e.now.Add(-71 * time.Hour)})
	e.t.set(transmission.Torrent{Hash: "done", PercentDone: 1, Files: []transmission.File{e.file(t, "done", "d/d.mkv")}, DoneDate: e.now.Add(-100 * time.Hour), Stopped: true})
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Equal(t, []string{"old"}, e.t.stopped)
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Equal(t, []string{"old"}, e.t.stopped, "not stopped twice")

	y := e.get(t, "young")
	assert.True(t, y.SeedingUntil.Equal(e.now.Add(time.Hour)))
	assert.False(t, y.SeedingDone)
	assert.True(t, e.get(t, "old").SeedingDone)
	assert.True(t, e.get(t, "done").SeedingDone)
}

func TestPollMakesDownloadsSequential(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "old", "Old", 2000)
	e.addMovie(t, "new", "New", 2000)
	e.addMovie(t, "fin", "Fin", 2000)
	e.t.set(transmission.Torrent{Hash: "old", PercentDone: 0.3})
	e.t.set(transmission.Torrent{Hash: "new", PercentDone: 0.3, Sequential: true})
	e.t.set(transmission.Torrent{Hash: "fin", PercentDone: 1, Files: []transmission.File{e.file(t, "fin", "f/f.mkv")}, DoneDate: e.now})
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Equal(t, []string{"old"}, e.t.seq, "only unfinished ones not in order yet")
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Equal(t, []string{"old"}, e.t.seq, "once")
}

func TestAddTorrentFilePicksFilesBeforeStart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// A .torrent file: Transmission knows the files as soon as it's added.
	e.t.nextFiles = []transmission.File{
		{Name: "D/D.mkv", Length: 2 * GB, Wanted: true}, {Name: "D/setup.exe", Length: 1, Wanted: true},
		{Name: "D/d.nfo", Length: 1, Wanted: true}, {Name: "D/D.en.srt", Length: 1, Wanted: true}}
	_, _, err := e.s.Add(ctx, alice, AddInput{File: []byte("x"), Kind: "movie", Title: "Dune", Year: 2021})
	require.NoError(t, err)
	assert.Equal(t, []string{"h1 +[] -[1]"}, e.t.wantSet, "the program is left out")
	assert.Empty(t, e.s.pending)
}

func TestMagnetPicksFilesOnceKnown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, _, err := e.s.Add(ctx, alice, AddInput{Magnet: "magnet:?xt=urn:btih:x", Kind: "movie", Title: "Dune", Year: 2021})
	require.NoError(t, err)
	require.NoError(t, e.s.CheckPending(ctx))
	assert.Empty(t, e.t.wantSet, "no file list yet")
	assert.True(t, e.s.pending["h1"])

	e.t.set(transmission.Torrent{Hash: "h1", Files: []transmission.File{
		{Name: "D/D.mkv", Length: 2 * GB, Wanted: true}, {Name: "D/D.mkv.exe", Length: 1, Wanted: true},
		{Name: "D/Sample/sample.mkv", Length: 10 << 20, Wanted: true}}})
	require.NoError(t, e.s.CheckPending(ctx))
	assert.Equal(t, []string{"h1 +[] -[1 2]"}, e.t.wantSet)
	assert.Empty(t, e.s.pending)
	require.NoError(t, e.s.CheckPending(ctx))
	assert.Len(t, e.t.wantSet, 1, "done once")
}

func TestPollPicksFiles(t *testing.T) {
	// Added before this rule, or a duplicate: the poll fixes it.
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 0.1, Sequential: true, Files: []transmission.File{
		{Name: "D/D.mkv", Length: 2 * GB, Wanted: true}, {Name: "D/virus.scr", Length: 1, Wanted: true}}})
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Equal(t, []string{"h1 +[] -[1]"}, e.t.wantSet)
	require.NoError(t, e.s.Poll(context.Background()))
	assert.Len(t, e.t.wantSet, 1, "nothing left to change")
}

func TestPauseAndResumeDownloading(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 0.5, Sequential: true})
	assert.True(t, e.get(t, "h1").CanPause())

	require.NoError(t, e.s.Pause(ctx, "h1"))
	assert.Equal(t, []string{"h1"}, e.t.stopped)
	d := e.get(t, "h1")
	assert.True(t, d.Paused)
	assert.False(t, d.CanPause())
	require.NoError(t, e.s.Poll(ctx))
	assert.Equal(t, "downloading", e.get(t, "h1").State, "a paused download just waits")
	assert.Empty(t, e.t.started, "the poll doesn't start it again")

	require.NoError(t, e.s.Resume(ctx, "h1"))
	assert.Equal(t, []string{"h1"}, e.t.started)
	assert.False(t, e.get(t, "h1").Paused)
	assert.ErrorIs(t, e.s.Pause(ctx, "nope"), ErrNotFound)
	assert.ErrorIs(t, e.s.Resume(ctx, "nope"), ErrNotFound)
}

func TestPauseSeeding(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 1, Files: []transmission.File{e.file(t, "h1", "d/d.mkv")}, DoneDate: e.now})
	require.NoError(t, e.s.Poll(ctx))
	require.Equal(t, "filed", e.get(t, "h1").State)
	assert.True(t, e.get(t, "h1").CanPause())

	require.NoError(t, e.s.Pause(ctx, "h1"))
	d := e.get(t, "h1")
	assert.True(t, d.Paused)
	assert.False(t, d.SeedingDone, "paused, not done")

	// Its seeding time ends while paused: it's done, and Resume leaves it stopped.
	e.now = e.now.Add(73 * time.Hour)
	d = e.get(t, "h1")
	assert.False(t, d.Paused)
	assert.True(t, d.SeedingDone)
	assert.False(t, d.CanPause())
	require.NoError(t, e.s.Resume(ctx, "h1"))
	assert.Empty(t, e.t.started)
}

func TestListLiveProgress(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 0.25, RateDownload: 5000,
		Size: 4000, Left: 3000, ETA: 2 * time.Minute})
	d := e.get(t, "h1")
	assert.Equal(t, 0.25, d.Percent)
	assert.Equal(t, int64(5000), d.RateDownload)
	assert.Equal(t, int64(4000), d.Size)
	assert.Equal(t, int64(1000), d.Done)
	assert.Equal(t, 2*time.Minute, d.ETA)
	assert.True(t, d.SeedingUntil.IsZero())
}

func TestEditDuringPollKeepsEdit(t *testing.T) {
	// A poll round that's filing while the title is corrected must not put
	// the old title back.
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2020)
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	e.t.gate, e.t.inGet = make(chan struct{}), make(chan struct{})
	gate := e.t.gate
	polled := make(chan error)
	go func() { polled <- e.s.Poll(context.Background()) }()
	<-e.t.inGet // the poll is inside its Transmission call
	edited := make(chan error)
	go func() { edited <- e.s.Edit(context.Background(), "h1", "movie", "Dune", 2021) }()
	time.Sleep(100 * time.Millisecond)
	close(gate)
	require.NoError(t, <-polled)
	require.NoError(t, <-edited)
	d := e.get(t, "h1")
	assert.Equal(t, 2021, d.Year)
	assert.Equal(t, "filed", d.State)
	assert.True(t, e.exists("library/Movies/Dune (2021)/Dune (2021).mkv"))
	assert.False(t, e.exists("library/Movies/Dune (2020)"))
}

func TestPollAlreadyInLibrary(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "a", "Dune", 2021)
	e.finish(t, "a", e.file(t, "a", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(context.Background()))
	e.addMovie(t, "b", "Dune", 2021)
	e.finish(t, "b", e.file(t, "b", "Other/Dune.mkv"))
	require.NoError(t, e.s.Poll(context.Background()))
	d := e.get(t, "b")
	assert.Equal(t, "filed", d.State)
	assert.Equal(t, "already in the library", d.Reason)
}

func TestAddTitleTooLong(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("Ж", 101) // 202 bytes: too long for a folder name with year and part
	_, _, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?x", Kind: "movie", Title: long, Year: 2000})
	var ie *InvalidError
	require.True(t, errors.As(err, &ie))
	assert.Equal(t, "error.title_long", ie.Key)
	assert.ErrorIs(t, e.s.Edit(context.Background(), "nope", "movie", long, 2000), ErrInvalid)
	_, _, err = e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?x", Kind: "movie", Title: strings.Repeat("Ж", 70), Year: 2000})
	assert.NoError(t, err)
}

// --- uploaded subtitles ---

var cp1251Privet = []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2} // "Привет"

func (e *env) addSeries(t *testing.T, hash, title string) {
	t.Helper()
	e.t.nextHash = hash
	_, _, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?xt=urn:btih:" + hash, Kind: "series", Title: title})
	require.NoError(t, err)
}

func (e *env) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, rel))
	require.NoError(t, err)
	return string(b)
}

func (e *env) subs(t *testing.T, hash string) []db.Subtitle {
	t.Helper()
	s, err := e.s.store.Subtitles(context.Background(), hash)
	require.NoError(t, err)
	return s
}

func TestFileSubtitles(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	require.NoError(t, e.s.storeUploads(ctx, "h1", "movie", []Upload{{Name: "dune.bg.srt", Lang: "bg", Data: cp1251Privet}}))
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(ctx))

	d := e.get(t, "h1")
	assert.Equal(t, "filed", d.State)
	assert.Equal(t, "", d.Reason)
	dst := "library/Movies/Dune (2021)/Dune (2021).bg.srt"
	assert.Equal(t, "Привет", e.read(t, dst))
	subs := e.subs(t, "h1")
	require.Len(t, subs, 1)
	a, err := os.Stat(filepath.Join(e.dir, dst))
	require.NoError(t, err)
	b, err := os.Stat(filepath.Join(e.dir, "subtitles", "h1", subs[0].ID+".srt"))
	require.NoError(t, err)
	assert.True(t, os.SameFile(a, b), "library file is a link to the kept one")
}

func TestFileSubtitleUnplaced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addSeries(t, "h1", "Show")
	require.NoError(t, e.s.storeUploads(ctx, "h1", "series", []Upload{{Name: "Show.S01E09.srt", Lang: "bg", Data: []byte("x")}}))
	e.finish(t, "h1", e.file(t, "h1", "Show.S01E01.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	d := e.get(t, "h1")
	assert.Equal(t, "filed", d.State)
	assert.Contains(t, d.Reason, "1 subtitle")
}

func TestEditKeepsSubtitles(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	require.NoError(t, e.s.storeUploads(ctx, "h1", "movie", []Upload{{Name: "a.srt", Lang: "bg", Data: []byte("x")}}))
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	require.NoError(t, e.s.Edit(ctx, "h1", "movie", "Dune Part One", 2021))
	assert.True(t, e.exists("library/Movies/Dune Part One (2021)/Dune Part One (2021).bg.srt"))
	assert.False(t, e.exists("library/Movies/Dune (2021)"))
	assert.Len(t, e.subs(t, "h1"), 1)
}

func TestRemoveDeletesSubtitles(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	require.NoError(t, e.s.storeUploads(ctx, "h1", "movie", []Upload{{Name: "a.srt", Lang: "bg", Data: []byte("x")}}))
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	require.NoError(t, e.s.Remove(ctx, "h1", true))
	assert.False(t, e.exists("subtitles/h1"))
	assert.False(t, e.exists("library/Movies/Dune (2021)"))
}

func TestLibrarySubtitleTracked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	video := "library/Movies/Dune (2021)/Dune (2021).mkv"
	require.NoError(t, e.s.LibrarySubtitle(ctx, video, "movie", Upload{Name: "one.srt", Lang: "bg", Data: []byte("one")}))
	require.NoError(t, e.s.LibrarySubtitle(ctx, video, "movie", Upload{Name: "two.srt", Lang: "bg", Data: []byte("two")}))
	dst := "library/Movies/Dune (2021)/Dune (2021).bg.srt"
	assert.Equal(t, "two", e.read(t, dst))
	assert.Len(t, e.subs(t, "h1"), 1)
	links, err := e.s.store.Links(ctx, "h1")
	require.NoError(t, err)
	n := 0
	for _, l := range links {
		if l == dst {
			n++
		}
	}
	assert.Equal(t, 1, n)
	entries, _ := os.ReadDir(filepath.Join(e.dir, "subtitles", "h1"))
	assert.Len(t, entries, 1, "the replaced upload's kept file is gone")
}

func TestLibrarySubtitleUntracked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	video := "library/Movies/Hand/Hand.mkv"
	require.NoError(t, os.MkdirAll(filepath.Join(e.dir, "library/Movies/Hand"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(e.dir, video), []byte("v"), 0o644))
	require.NoError(t, e.s.LibrarySubtitle(ctx, video, "movie", Upload{Name: "x.srt", Lang: "en", Data: cp1251Privet}))
	assert.Equal(t, "Привет", e.read(t, "library/Movies/Hand/Hand.en.srt"))
}

func TestLibrarySubtitlePinned(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addSeries(t, "h1", "Show")
	e.finish(t, "h1", e.file(t, "h1", "Season 1/03 - Title.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	video := "library/Shows/Show/Season 1/03 - Title.mkv"
	require.True(t, e.exists(video))
	require.NoError(t, e.s.LibrarySubtitle(ctx, video, "series", Upload{Name: "x.srt", Lang: "bg", Data: []byte("x")}))
	assert.True(t, e.exists("library/Shows/Show/Season 1/03 - Title.bg.srt"))
	subs := e.subs(t, "h1")
	require.Len(t, subs, 1)
	assert.Equal(t, video, subs[0].Video)
}

func TestLibrarySubtitleEpisodeCode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addSeries(t, "h1", "Show")
	e.finish(t, "h1", e.file(t, "h1", "Show.S01E02.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	video := "library/Shows/Show/Show.S01E02.mkv"
	require.NoError(t, e.s.LibrarySubtitle(ctx, video, "series", Upload{Name: "whatever.srt", Lang: "bg", Data: []byte("x")}))
	subs := e.subs(t, "h1")
	require.Len(t, subs, 1)
	assert.Equal(t, "S01E02", subs[0].Name)
	assert.Equal(t, "", subs[0].Video)
	assert.True(t, e.exists("library/Shows/Show/Show.S01E02.bg.srt"))
}

func TestLibrarySubtitleRejects(t *testing.T) {
	e := newEnv(t)
	err := e.s.LibrarySubtitle(context.Background(), "library/Movies/X/X.mkv", "movie", Upload{Name: "x.txt", Lang: "bg", Data: []byte("x")})
	assert.ErrorIs(t, err, ErrInvalid)
	err = e.s.LibrarySubtitle(context.Background(), "library/Movies/X/X.mkv", "movie", Upload{Name: "x.srt", Lang: "de", Data: []byte("x")})
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestAddWithSubtitles(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.t.nextHash = "h1"
	_, _, err := e.s.Add(ctx, alice, AddInput{Magnet: "magnet:?xt=urn:btih:h1", Kind: "movie", Title: "Dune", Year: 2021,
		Subtitles: []Upload{{Name: "dune.srt", Lang: "bg", Data: []byte("x")}}})
	require.NoError(t, err)
	assert.Len(t, e.subs(t, "h1"), 1)
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	assert.True(t, e.exists("library/Movies/Dune (2021)/Dune (2021).bg.srt"))
}

func TestAddSeriesSubtitleWithoutCode(t *testing.T) {
	e := newEnv(t)
	e.t.nextHash = "h1"
	_, _, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?xt=urn:btih:h1", Kind: "series", Title: "Show",
		Subtitles: []Upload{{Name: "Show.S01E01.srt", Lang: "bg", Data: []byte("x")}, {Name: "x.srt", Lang: "bg", Data: []byte("x")}}})
	var in *InvalidError
	require.ErrorAs(t, err, &in)
	assert.Equal(t, "error.sub_episode", in.Key)
	assert.Equal(t, "x.srt", in.Arg)
	assert.Equal(t, 0, e.t.adds, "nothing started")
	list, _ := e.s.List(context.Background())
	assert.Empty(t, list)
}

func TestAddSubtitlesToFiledDuplicate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	e.t.nextHash = "h1"
	_, existing, err := e.s.Add(ctx, alice, AddInput{Magnet: "magnet:?xt=urn:btih:h1", Kind: "movie", Title: "Dune", Year: 2021,
		Subtitles: []Upload{{Name: "dune.srt", Lang: "en", Data: []byte("x")}}})
	require.NoError(t, err)
	assert.True(t, existing)
	assert.True(t, e.exists("library/Movies/Dune (2021)/Dune (2021).en.srt"), "placed right away")
}

func TestAddTooManySubtitles(t *testing.T) {
	e := newEnv(t)
	ups := make([]Upload, 31)
	for i := range ups {
		ups[i] = Upload{Name: "a.srt", Lang: "bg", Data: []byte("x")}
	}
	_, _, err := e.s.Add(context.Background(), alice, AddInput{Magnet: "magnet:?x", Kind: "movie", Title: "X", Year: 2000, Subtitles: ups})
	assert.ErrorIs(t, err, ErrInvalid)
	assert.Equal(t, 0, e.t.adds)
}

func TestAddDuplicateChecksExistingKind(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addSeries(t, "h1", "Show")
	e.t.nextHash = "h1"
	_, _, err := e.s.Add(ctx, alice, AddInput{Magnet: "magnet:?xt=urn:btih:h1", Kind: "movie", Title: "Show", Year: 2020,
		Subtitles: []Upload{{Name: "Show.bg.srt", Lang: "bg", Data: []byte("x")}}})
	var in *InvalidError
	require.ErrorAs(t, err, &in)
	assert.Equal(t, "error.sub_episode", in.Key)
	assert.Empty(t, e.subs(t, "h1"))
}

func TestAddDuplicateReportsUnplaced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addSeries(t, "h1", "Show")
	e.finish(t, "h1", e.file(t, "h1", "Show.S01E01.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	e.t.nextHash = "h1"
	_, _, err := e.s.Add(ctx, alice, AddInput{Magnet: "magnet:?xt=urn:btih:h1", Kind: "series", Title: "Show",
		Subtitles: []Upload{{Name: "Show.S01E09.srt", Lang: "bg", Data: []byte("x")}}})
	require.NoError(t, err)
	assert.Contains(t, e.get(t, "h1").Reason, "1 subtitle")
}

func TestLibrarySubtitleOtherFormatReplaces(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	video := "library/Movies/Dune (2021)/Dune (2021).mkv"
	require.NoError(t, e.s.LibrarySubtitle(ctx, video, "movie", Upload{Name: "a.srt", Lang: "bg", Data: []byte("one")}))
	require.NoError(t, e.s.LibrarySubtitle(ctx, video, "movie", Upload{Name: "a.ass", Lang: "bg", Data: []byte("two")}))
	assert.False(t, e.exists("library/Movies/Dune (2021)/Dune (2021).bg.srt"))
	assert.True(t, e.exists("library/Movies/Dune (2021)/Dune (2021).bg.ass"))
	links, err := e.s.store.Links(ctx, "h1")
	require.NoError(t, err)
	assert.NotContains(t, links, "library/Movies/Dune (2021)/Dune (2021).bg.srt")

	hand := "library/Movies/Hand/Hand.mkv"
	require.NoError(t, os.MkdirAll(filepath.Join(e.dir, "library/Movies/Hand"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(e.dir, hand), []byte("v"), 0o644))
	require.NoError(t, e.s.LibrarySubtitle(ctx, hand, "movie", Upload{Name: "a.srt", Lang: "bg", Data: []byte("one")}))
	require.NoError(t, e.s.LibrarySubtitle(ctx, hand, "movie", Upload{Name: "a.vtt", Lang: "bg", Data: []byte("two")}))
	assert.False(t, e.exists("library/Movies/Hand/Hand.bg.srt"))
	assert.True(t, e.exists("library/Movies/Hand/Hand.bg.vtt"))
}

func TestRemoveKeepFilesDropsKeptSubtitles(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	require.NoError(t, e.s.storeUploads(ctx, "h1", "movie", []Upload{{Name: "a.srt", Lang: "bg", Data: []byte("bg")}}))
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	require.NoError(t, e.s.Remove(ctx, "h1", false))
	assert.False(t, e.exists("subtitles/h1"), "the app's own copies go")
	assert.Equal(t, "bg", e.read(t, "library/Movies/Dune (2021)/Dune (2021).bg.srt"), "the library keeps its subtitle")
}

func TestReplaceKeepsOldWhenSavingFails(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	require.NoError(t, e.s.storeUploads(ctx, "h1", "movie", []Upload{{Name: "a.srt", Lang: "bg", Data: []byte("old")}}))
	dir := filepath.Join(e.dir, "subtitles", "h1")
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := e.s.storeUploads(ctx, "h1", "movie", []Upload{{Name: "b.srt", Lang: "bg", Data: []byte("new")}})
	require.Error(t, err)
	subs := e.subs(t, "h1")
	require.Len(t, subs, 1, "the earlier upload is still recorded")
	assert.Equal(t, "old", e.read(t, "subtitles/h1/"+subs[0].ID+".srt"))
}

func TestRefileLeavesHandPlacedSubtitle(t *testing.T) {
	// A file someone put at the subtitle's place by hand is never
	// overwritten when the download is filed; the download still files.
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	require.NoError(t, e.s.storeUploads(ctx, "h1", "movie", []Upload{{Name: "a.srt", Lang: "bg", Data: []byte("upload")}}))
	dst := filepath.Join(e.dir, "library/Movies/Dune (2021)/Dune (2021).bg.srt")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(dst, []byte("by hand"), 0o644))
	e.finish(t, "h1", e.file(t, "h1", "D/Dune.mkv"))
	require.NoError(t, e.s.Poll(ctx))
	assert.Equal(t, "filed", e.get(t, "h1").State)
	assert.Equal(t, "by hand", e.read(t, "library/Movies/Dune (2021)/Dune (2021).bg.srt"))
}

func TestListStats(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 0.5, RateDownload: 10, RateUpload: 1000, Uploaded: 5000,
		Ratio: 0.5, Peers: 12, PeersSending: 8, Seeders: 45, QueuePos: 3})
	d := e.get(t, "h1")
	assert.True(t, d.Live)
	assert.Equal(t, int64(1000), d.RateUpload)
	assert.Equal(t, int64(5000), d.Uploaded)
	assert.Equal(t, 0.5, d.Ratio)
	assert.Equal(t, 12, d.Peers)
	assert.Equal(t, 8, d.PeersSending)
	assert.Equal(t, 45, d.Seeders)
	assert.True(t, d.InOrder, "in order by default")
	assert.Equal(t, "", d.Stuck)
}

func TestStuckReasons(t *testing.T) {
	for name, c := range map[string]struct {
		t      transmission.Torrent
		any    bool // marked any-order first
		reason string
	}{
		"queued":       {t: transmission.Torrent{Queued: true, QueuePos: 2, Peers: 3}, reason: "queued"},
		"tracker":      {t: transmission.Torrent{TrackerError: "Connection failed", Peers: 3}, reason: "tracker"},
		"no peers":     {t: transmission.Torrent{Seeders: 45}, reason: "no_peers"},
		"in order":     {t: transmission.Torrent{Peers: 3, Sequential: true}, reason: "in_order"},
		"any order":    {t: transmission.Torrent{Peers: 3}, any: true, reason: "not_sending"},
		"moving":       {t: transmission.Torrent{Peers: 3, RateDownload: 50, Sequential: true}, reason: ""},
		"sending slow": {t: transmission.Torrent{Peers: 3, PeersSending: 1, Sequential: true}, reason: ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.addMovie(t, "h1", "Dune", 2021)
			c.t.Hash, c.t.PercentDone = "h1", 0.3
			e.t.set(c.t)
			if c.any {
				require.NoError(t, e.s.AnyOrder(context.Background(), "h1"))
				e.t.set(c.t)
			}
			d := e.get(t, "h1")
			assert.Equal(t, c.reason, d.Stuck)
			if c.reason == "queued" {
				assert.Equal(t, 2, d.QueuePos)
			}
			if c.reason == "tracker" {
				assert.Equal(t, "Connection failed", d.TrackerError)
			}
		})
	}
}

func TestStuckOnlyWhileDownloading(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "p", "Paused", 2021)
	e.t.set(transmission.Torrent{Hash: "p", PercentDone: 0.3})
	require.NoError(t, e.s.Pause(ctx, "p"))
	assert.Equal(t, "", e.get(t, "p").Stuck)

	e.addMovie(t, "f", "Filed", 2021)
	e.t.set(transmission.Torrent{Hash: "f", PercentDone: 1, Files: []transmission.File{e.file(t, "f", "f/f.mkv")}, DoneDate: e.now})
	require.NoError(t, e.s.Poll(ctx))
	require.Equal(t, "filed", e.get(t, "f").State)
	assert.Equal(t, "", e.get(t, "f").Stuck, "seeding with no peers isn't stuck")
}

func TestAnyOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 0.3, Sequential: true})
	require.NoError(t, e.s.AnyOrder(ctx, "h1"))
	assert.Equal(t, []string{"h1"}, e.t.seqOff)
	assert.False(t, e.get(t, "h1").InOrder)
	assert.ErrorIs(t, e.s.AnyOrder(ctx, "nope"), ErrNotFound)
}

func TestPollKeepsAnyOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 0.3, Sequential: true})
	require.NoError(t, e.s.AnyOrder(ctx, "h1"))
	require.NoError(t, e.s.Poll(ctx))
	require.NoError(t, e.s.Poll(ctx))
	assert.Empty(t, e.t.seq, "the poll leaves it in any order")
	assert.False(t, e.get(t, "h1").InOrder)
}

func TestStartNow(t *testing.T) {
	e := newEnv(t)
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", Queued: true, QueuePos: 2})
	require.NoError(t, e.s.StartNow(context.Background(), "h1"))
	assert.Equal(t, []string{"h1"}, e.t.startNow)
	assert.ErrorIs(t, e.s.StartNow(context.Background(), "nope"), ErrNotFound)
}

func TestPortTestThrottled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 0.3, Sequential: true, Peers: 3, PeersSending: 1, RateDownload: 10})
	require.NoError(t, e.s.Poll(ctx))
	assert.Equal(t, 0, e.t.portCalls, "nothing stuck without peers")

	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 0.3, Sequential: true, Seeders: 45})
	require.NoError(t, e.s.Poll(ctx))
	assert.Equal(t, 1, e.t.portCalls)
	d := e.get(t, "h1")
	assert.Equal(t, "no_peers", d.Stuck)
	assert.True(t, d.PortClosed)
	assert.Equal(t, 51413, d.Port)

	e.now = e.now.Add(time.Minute)
	require.NoError(t, e.s.Poll(ctx))
	assert.Equal(t, 1, e.t.portCalls, "at most every 10 minutes")
	e.now = e.now.Add(10 * time.Minute)
	e.t.portOpen = true
	require.NoError(t, e.s.Poll(ctx))
	assert.Equal(t, 2, e.t.portCalls)
	assert.False(t, e.get(t, "h1").PortClosed)
}

func TestPortClosedOnlyOnNoPeers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "a", "A", 2021)
	e.addMovie(t, "b", "B", 2021)
	e.t.set(transmission.Torrent{Hash: "a", PercentDone: 0.3})
	e.t.set(transmission.Torrent{Hash: "b", PercentDone: 0.3, Peers: 2, Sequential: true})
	require.NoError(t, e.s.Poll(ctx))
	assert.True(t, e.get(t, "a").PortClosed)
	assert.False(t, e.get(t, "b").PortClosed)
}

func TestPortTestErrorIgnored(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addMovie(t, "h1", "Dune", 2021)
	e.t.set(transmission.Torrent{Hash: "h1", PercentDone: 0.3})
	e.t.portErr = errors.New("port-test failed")
	assert.NoError(t, e.s.Poll(ctx), "a failed port test doesn't fail the poll")
	d := e.get(t, "h1")
	assert.Equal(t, "no_peers", d.Stuck)
	assert.False(t, d.PortClosed)
}
