# Subtitle Upload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upload subtitle files when adding a download or from a movie/episode page. Each file is renamed to its video's name plus `.bg`/`.en` and placed in the library, and Bulgarian becomes the default text subtitle language.

**Architecture:** Uploads are converted to UTF-8 and stored as masters under `DATA_DIR/subtitles/<hash>/<id><ext>`, with a `download_subtitles` row per upload. The pure function `library.PlaceSubtitles` turns the stored rows plus a filing's links into extra hard links, and they go through the existing `Apply`/`AddLinks`/`Undo` bookkeeping. Library uploads reuse the same pieces immediately. Files put in the library by hand get a plain converted copy, which isn't tracked.

**Tech Stack:** Go 1.x, gin, pgx/goose (Postgres), html/template, goquery and testify tests. `make test` needs Docker.

**Spec:** `docs/superpowers/specs/2026-11-20-subtitle-upload-design.md`

## Global Constraints

- Text formats only: `.srt .ass .ssa .vtt`. At most 2 MB each and 30 per Add submit. Empty files and files with a NUL byte are rejected.
- Languages are `bg` and `en` only; the filename code is `.<lang><ext>`. The form defaults to `bg`.
- Encoding: valid UTF-8 is kept, minus a leading BOM. Anything else is decoded as Windows-1251 and written as UTF-8.
- Episode codes accepted: `S01E03`, `s1e3`, `1x03`. They're normalised to `S01E03`.
- Uploads beat a torrent's own subtitle on the same library path. Re-uploading the same (download, lang, target) replaces the old one.
- Targets are re-derived from the stored `name` at every filing under the download's current kind. Pinned (`video` ≠ "") rows place only at that exact library path.
- Unplaced subtitles never fail a download. The `Reason` of a filed download says "N subtitles didn't match a video" (EN and BG texts).
- A default (never-chosen) subtitle preference means Bulgarian **text** tracks only. An explicit `bul` keeps today's behaviour.
- No zips, no picture subtitle formats, no settings page.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- A Bulgarian `.srt` in Windows-1251 with Cyrillic must come out as readable UTF-8 Cyrillic, and a UTF-8 file with a BOM must lose the BOM (Task 1 tests).
- Edit changes a series' title. The uploaded subtitle must reappear at the new path, its master must survive, and no stray files must be left at the old path (Task 4 test).
- Remove with "delete files" must leave no `subtitles/<hash>/` folder and no empty library folders behind (Task 4 test).
- A library upload replacing an earlier upload leaves exactly one file at the path, and it has the new content (Task 4 test).
- An Add with one bad subtitle (e.g. a series file with no episode code) must start nothing and store nothing (Task 5 test).

---

### Task 1: Subtitle file handling (validate, convert, targets)

**Files:**
- Create: `internal/subtitles/subtitles.go`, `internal/subtitles/subtitles_test.go`

**Interfaces:**
- Produces:
  - `var ErrType, ErrSize, ErrEmpty, ErrBinary error`
  - `const MaxSize = 2 << 20`
  - `func Prepare(name string, data []byte) (ext string, utf8 []byte, err error)`: validates the extension (lowercased, returned with the dot), the size and content, and converts the encoding.
  - `func EpisodeCode(name string) (string, bool)`: returns `"S01E03"` from `Show.S01E03.srt`, `show s1e3`, `Show 1x03`. Matching is case-insensitive and needs a non-alphanumeric character or string end before and after the code. `1x03` must not match inside `1080p` or `x264`.
  - `func PartCode(name string) (string, bool)`: returns `"part2"` when the name has a part marker (cd2, part2, disc2, same as `library.partMarker`). Export `library.PartNumber(stem string) (int, bool)` and reuse it so the two never drift.
  - `func Target(kind, name string) (target string, ok bool)`: movie → `"part"+N` if the name has a part marker, else `""` (always ok). Series → the episode code, with ok false when there is none.

- [ ] **Step 1: Write the tests.**
  - `TestPrepareCP1251`: `[]byte{0xCF,0xF0,0xE8,0xE2,0xE5,0xF2}` ("Привет" in cp1251) inside an SRT body comes out as UTF-8 `Привет`.
  - `TestPrepareUTF8BOM`: input `"\xEF\xBB\xBFhello"` gives `"hello"`.
  - `TestPrepareRejects`: `x.txt` → ErrType, an empty file → ErrEmpty, 2 MB + 1 byte → ErrSize, data with `\x00` → ErrBinary. `.SRT` is accepted and gives ext `.srt`.
  - `TestEpisodeCode`: table with `Show.S01E03.720p.srt→S01E03`, `show s1e3.srt→S01E03`, `Show.1x03.srt→S01E03`, `Show.S10E120.srt→S10E120`, and `Movie.1080p.x264.srt→none`.
  - `TestTarget`: `movie,"Dune CD2.srt"→part2`, `movie,"Dune.srt"→""`, `series,"x.S02E05.srt"→S02E05`, `series,"x.srt"→not ok`.
- [ ] **Step 2: Run them.** `go test ./internal/subtitles/` → FAIL (package missing).
- [ ] **Step 3: Implement.** Use a 128-entry cp1251 table in code (0x80–0xFF → rune) rather than adding x/text. Validate UTF-8 with `utf8.Valid`. Add `library.PartNumber` in `plan.go` and use it from `numberedParts`.
- [ ] **Step 4: Run.** `go test ./internal/subtitles/ ./internal/library/` → PASS.
- [ ] **Step 5: Commit** `feat: subtitle file checks, UTF-8 conversion and episode codes`

### Task 2: Placement in the library plan

**Files:**
- Create: `internal/library/subtitles.go`, `internal/library/subtitles_test.go`

**Interfaces:**
- Consumes: `subtitles.Target`, `subtitles.EpisodeCode`, the `Link` type.
- Produces:
  ```go
  type Subtitle struct{ ID, Lang, Ext, Name, Video string } // Video: pinned library path or ""
  func SubtitleFile(hash string, s Subtitle) string            // "subtitles/<hash>/<id><ext>"
  func PlaceSubtitles(kind, hash string, links []Link, subs []Subtitle) (out []Link, unplaced int)
  ```
  `out` is `links` with conflicting torrent-subtitle links removed and the subtitle links appended. Videos are the links whose `To` has a video extension (reuse `videoExt`). A movie's main video is the one directly in the movie folder (not `extras/`) whose stem has no ` - partN`; for a multi-part movie, target `""` means part 1. `partN` matches the stem ending in ` - partN`. A series target matches the video whose base name's `EpisodeCode` equals it; on a tie the shorter path wins. Pinned rows match `To == Video` exactly. Each placed link is `{From: SubtitleFile(hash,s), To: dir/stem + "." + Lang + Ext}`. If two subs map to the same `To`, the later one in `subs` wins (rows come ordered by `added_at`).

- [ ] **Step 1: Write the tests.**
  - `TestPlaceMovie`: links for `library/Movies/Dune (2021)/Dune (2021).mkv` plus a torrent sub at `…/Dune (2021).bg.srt`. Sub `{ID:"a",Lang:"bg",Ext:".srt",Name:"whatever.srt"}` gives the torrent sub link removed, `{subtitles/h/a.srt → library/Movies/Dune (2021)/Dune (2021).bg.srt}` added, and unplaced 0.
  - `TestPlaceMultiPart`: parts `X - part1.mkv`, `X - part2.mkv`. Names `x cd2.srt` → part2 and `x.srt` → part1.
  - `TestPlaceSeries`: videos `library/Shows/S/Season 1/S.S01E03.mkv` and `S.S01E04.mkv`. Sub name `S.1x04.srt` lands next to E04. Sub name `S.S02E01.srt` → unplaced 1.
  - `TestPlacePinned`: Video `library/Shows/S/03 - Title.mkv` places there when that link exists; when it doesn't → unplaced 1.
  - `TestPlaceKindChange`: the same sub named `Dune.S01E01.srt` under kind "movie" places next to the main video (the movie target is `""`).
- [ ] **Step 2: Run.** `go test ./internal/library/ -run TestPlace` → FAIL.
- [ ] **Step 3: Implement** in `internal/library/subtitles.go`.
- [ ] **Step 4: Run.** `go test ./internal/library/` → PASS.
- [ ] **Step 5: Commit** `feat(library): place uploaded subtitles next to their videos`

### Task 3: Storage (migration and store methods)

**Files:**
- Create: `internal/db/migrations/00003_download_subtitles.sql` (Up/Down, columns exactly as the spec's table).
- Create: `internal/db/subtitles.go`
- Test: through the Task 4 service tests, plus `TestMigrations` (already runs every migration up and down).

**Interfaces:**
- Produces, on `*db.Store`:
  ```go
  type Subtitle struct{ Hash, ID, Lang, Ext, Name, Video string; AddedAt time.Time }
  func (s *Store) AddSubtitle(ctx context.Context, sub Subtitle) error
  func (s *Store) Subtitles(ctx context.Context, hash string) ([]Subtitle, error) // ORDER BY added_at, id
  func (s *Store) DeleteSubtitle(ctx context.Context, hash, id string) error
  func (s *Store) LinkOwner(ctx context.Context, path string) (hash string, err error) // ErrNotFound when none
  func (s *Store) DeleteLink(ctx context.Context, hash, path string) error
  ```
  Follow the query style in `internal/db/downloads.go`.

- [ ] **Step 1: Write the migration and methods.** The compile-time use comes in Task 4.
- [ ] **Step 2: Run.** `make test` → PASS (`TestMigrations` covers 00003 up and down).
- [ ] **Step 3: Commit** `feat(db): table for uploaded subtitles`

### Task 4: Downloads service (filing, removal, library uploads)

**Files:**
- Modify: `internal/downloads/service.go` (Store interface, `file`, `Remove`, new methods)
- Test: `internal/downloads/service_test.go`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces:
  ```go
  type Upload struct{ Name, Lang string; Data []byte }
  // Store interface gains AddSubtitle, Subtitles, DeleteSubtitle, LinkOwner, DeleteLink.
  func (s *Service) LibrarySubtitle(ctx context.Context, video string, kind string, up Upload) error
  ```
  - `video` is a library path relative to DATA_DIR, e.g. `library/Movies/X/X.mkv`. `kind` is "movie" or "series".
  - Errors are `invalid("error.sub_type"|"error.sub_size"|"error.sub_empty"|"error.sub_lang")`, mapped from `subtitles.Err*`.
  - The private helper `storeUploads(ctx, hash, kind string, ups []Upload) error` is used by Add (Task 5) and LibrarySubtitle. For each upload it validates, then for a `(hash, lang, target)` that already exists it deletes the old row, master and library link, writes the master file, and inserts the row.

- [ ] **Step 1: Write the failing tests.** Use `e.s.storeUploads` directly for the add-time path.
  - `TestFileSubtitles`: movie `h1` with `Dune.mkv`. Store an upload `{Name:"dune.bg.srt",Lang:"bg",Data:cp1251Bytes}`, then finish and poll. `library/Movies/Dune (2021)/Dune (2021).bg.srt` exists, is UTF-8, and is the same file as `subtitles/h1/<id>.srt` (`os.SameFile`). Reason is "".
  - `TestFileSubtitleUnplaced`: series with one episode S01E01 and an upload named `S01E09.srt` → state "filed", and Reason contains "1".
  - `TestEditKeepsSubtitles`: after filing, `Edit` to title "Dune Part One" → the subtitle is at the new folder, the old folder is gone, and the master remains.
  - `TestRemoveDeletesSubtitles`: Remove with deleteFiles → no `subtitles/h1`, no `library/Movies/Dune (2021)`.
  - `TestLibrarySubtitleTracked`: after filing, `LibrarySubtitle(ctx, "library/Movies/Dune (2021)/Dune (2021).mkv", "movie", up1)` and then the same again with up2 (same lang) → exactly one `.bg.srt` with up2's content, one row, and `download_links` holds it once.
  - `TestLibrarySubtitleUntracked`: write `library/Movies/Hand/Hand.mkv` by hand, then `LibrarySubtitle` → `Hand.bg.srt` exists, with no rows.
  - `TestLibrarySubtitlePinned`: a series episode file without a code (`Show/Season 1/03 - Title.mkv`) → the row has `Video` set and the file is placed.
- [ ] **Step 2: Run.** `make test` → the new tests FAIL.
- [ ] **Step 3: Implement.**
  - In `file`: after `Plan`, load `Subtitles(hash)`, call `library.PlaceSubtitles(r.Kind, r.Hash, links, subs)`, apply and record all links, and set `Reason` to `T("en","download.subs_unplaced")`-style text. Use a plain English string "N subtitles didn't match a video" in `Reason`, as other reasons are English; the downloads page may translate it later.
  - In `Remove` with deleteFiles: after `Undo`, `os.RemoveAll(DATA_DIR/subtitles/<hash>)`.
  - `LibrarySubtitle`:
    - **Owner found** (`LinkOwner`):
      - Pinned or not: a series video whose base has no `EpisodeCode` gets a row with `Video = video` and `Name = base`. Otherwise `Name` is the synthetic target (`"S01E03"`, `"part2"` or `"movie"`).
      - Replace any same (lang, target/video) row: Undo its link, `DeleteLink`, delete the master and row.
      - Write the master, `Apply` the single link (remove an existing destination first; it's the old upload or a torrent sub, and uploads win), then `AddLinks`.
    - **No owner:** write the converted data straight to `<dir>/<stem>.<lang><ext>`, replacing what's there.
    - Hold `s.mu` throughout.
- [ ] **Step 4: Run.** `make test` → PASS. `make check` → clean.
- [ ] **Step 5: Commit** `feat(downloads): store uploaded subtitles and place them when filing`

### Task 5: Add page upload

**Files:**
- Modify: `internal/downloads/service.go` (`AddInput.Subtitles []Upload`; Add validates them all before contacting Transmission, then stores them after the row exists, including when duplicate; for an already filed download it places them now with the same link logic as `LibrarySubtitle`'s owner path)
- Modify: `internal/handlers/downloads.go` (`postAdd`: read `subtitles` multipart files and `subtitle_lang`; raise the `MaxBytesReader` limit by `30*subtitles.MaxSize`)
- Modify: `internal/templates/pages/add.html`, `internal/i18n/en.json`, `internal/i18n/bg.json`
- Test: `internal/downloads/service_test.go`, `internal/handlers/pages_test.go`

**Interfaces:**
- Consumes: `Upload`, `storeUploads`, `subtitles.Target`.
- Produces: form fields `subtitles` (multiple files, accept `.srt,.ass,.ssa,.vtt`) and `subtitle_lang` (`bg` selected, `en`). New i18n keys: `add.subtitles`, `add.subtitles_hint` ("For a show, each file name needs its episode number, e.g. S01E03." / BG equivalent), `lang.bg`, `lang.en`, `error.sub_type`, `error.sub_size`, `error.sub_empty`, `error.sub_lang`, `error.sub_episode` ("%s has no episode number").

- [ ] **Step 1: Write the failing tests.**
  - `TestAddWithSubtitles` (service): a movie Add with one upload → one row. Finish and poll → placed.
  - `TestAddSeriesSubtitleWithoutCode` (service): a series Add with `x.srt` → `ErrInvalid` with key `error.sub_episode`, **and** the fake torrents received no Add (`len(e.t.added)==0`), and there are no rows.
  - `TestAddPageSubtitleField` (handlers): GET /add has `input[name=subtitles][multiple]` and `select[name=subtitle_lang] option[value=bg][selected]`.
  - `TestPostAddWithSubtitle` (handlers): a multipart POST with a magnet, title and one `.srt` → 303 to /downloads, and the fake Downloads received `AddInput.Subtitles` with len 1 and Lang "bg".
- [ ] **Step 2: Run.** `make test` → FAIL.
- [ ] **Step 3: Implement.** Make the `error.sub_episode` message carry the filename: give `InvalidError` an optional `Arg string` and have `errorText` format it with `%s` when set.
- [ ] **Step 4: Run.** `make test`, `make check` → PASS.
- [ ] **Step 5: Commit** `feat(web): attach subtitles when adding a download`

### Task 6: Library page upload

**Files:**
- Modify: `internal/jellyfin/items.go` (`RefreshItem(ctx, id string) error` → `POST /Items/{id}/Refresh?MetadataRefreshMode=Default&Recursive=false`)
- Modify: `internal/handlers/deps.go` (the `Downloads` interface gains `LibrarySubtitle`; a new `Library interface{ RefreshItem(ctx, id string) error; Refresh(ctx) error }` field on `Deps`, wired in `cmd/` to the same Jellyfin client)
- Modify: `internal/handlers/web.go` (route `POST /subtitles/:id`), `internal/handlers/pages.go` (handler `postSubtitles`, movie view `Parts int`, `?subs=1` notice)
- Modify: `internal/templates/pages/movie.html`, `internal/templates/pages/show.html`, `internal/static/css/media.css`, i18n
- Test: `internal/handlers/pages_test.go`, `internal/jellyfin/*_test.go` (RefreshItem request shape, following existing client tests)

**Interfaces:**
- Consumes: `downloads.Service.LibrarySubtitle`, `Catalog.Item`, `Catalog.Parts`.
- Produces:
  - `func libraryPath(jellyfinPath string) (string, bool)`: the part from `/library/` on, joined as `library/…`. Returns false without one, or with `..` in it.
  - Markup: on the movie page, `<details class="subs"><summary>Add subtitles</summary><form method=post action=/subtitles/{id} enctype=multipart/form-data>` with file, lang select (bg default) and, when Parts>1, a `part` select. On the show page, each episode row gets the same `<details>` with action `/subtitles/{episodeId}`. The episode row's existing `<a>` stays; the details sits beside it.
  - `postSubtitles`:
    1. `Catalog.Item(id)`. Accept only Movie and Episode.
    2. For a movie with `part=N>1`, use `Catalog.Parts` item N's Path.
    3. Map the path with `libraryPath`, read the file (2 MB limit), and call `LibrarySubtitle(ctx, rel, kind, Upload{…})`.
    4. Call `RefreshItem(id)`, falling back to `Refresh`, and log either error.
    5. Redirect 303 to the movie or show page with `?subs=1`.
    6. On an invalid input, redirect with `?subs_error=<key>` so the page shows the translated error.
  - i18n keys: `subs.add`, `subs.file`, `subs.part`, `subs.added`, `error.not_in_library`.

- [ ] **Step 1: Write the failing tests.**
  - `TestLibraryPath`: `/data/library/Movies/X/X.mkv` → `library/Movies/X/X.mkv`. `/media/other/x.mkv` → false. `/data/library/../etc` → false.
  - `TestMoviePageSubtitleForm`: the form exists with action `/subtitles/<id>`. A part select is present only for a 2-part movie.
  - `TestShowPageEpisodeSubtitleForm`: one form per episode.
  - `TestPostSubtitlesMovie`: a multipart POST → the fake Downloads got `LibrarySubtitle(rel "library/Movies/…", "movie", Lang "bg")`, RefreshItem was called with the id, and the response is 303 to `/movies/<id>?subs=1`.
  - `TestPostSubtitlesRejectsSeries`: a Series item id → 400.
  - `TestRefreshItem` (jellyfin): the request is `POST /Items/<id>/Refresh` with those query values.
- [ ] **Step 2: Run.** `make test` → FAIL.
- [ ] **Step 3: Implement.** Keep the forms working without JavaScript. Style the `<details>` so it doesn't push the episode row layout around on phones.
- [ ] **Step 4: Run.** `make test`, `make check` → PASS.
- [ ] **Step 5: Browser check.** Render the movie and show pages through the handler-dump trick (a throwaway `go test` that writes the HTML; fakes provide items), open them at 360px and desktop width in Chrome, and confirm the forms look right and don't break the rows.
- [ ] **Step 6: Commit** `feat(web): add subtitles from a movie or episode page`

### Task 7: Bulgarian default for text subtitles

**Files:**
- Modify: `internal/watching/service.go` (`PickTracks`)
- Test: `internal/watching/service_test.go` (`TestPickTracks`)

**Interfaces:**
- Produces: `PickTracks` with `p.SubtitleLang == ""` picks the first track with `Lang == "bul"` and `!Image`. `"off"` gives none. An explicit `"bul"` keeps today's behaviour, picture tracks included.

- [ ] **Step 1: Add cases to `TestPickTracks`.**
  - Empty prefs with tracks `[{Index:3,Lang:"eng"},{Index:4,Lang:"bul",Image:true},{Index:5,Lang:"bul"}]` → sub 5.
  - Empty prefs with only the image bul track → nil.
  - `SubtitleLang:"off"` → nil.
  - `SubtitleLang:"bul"` with only the image track → 4.
- [ ] **Step 2: Run.** `make test` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** `make test` → PASS.
- [ ] **Step 5: Commit** `feat: Bulgarian text subtitles by default`
