# Subtitle Zips Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Both subtitle upload forms take a `.zip`; subtitles inside are handled like separate uploads, and two or more for the same video and language are kept side by side as labelled tracks.

**Architecture:** `subtitles.Unzip` and `subtitles.Labels` are pure helpers. The handlers unpack zips into `downloads.Upload`s. The downloads service keeps one upload's subtitles as a batch (labels, no replacing each other, replacing older uploads and their library links). `library.PlaceSubtitles` names labelled files `<stem>.<label>.<lang><ext>`.

**Tech Stack:** Go 1.23, `archive/zip`, Postgres (goose migration), gin, html/template.

**Spec:** `docs/superpowers/specs/2026-11-20-subtitle-zip-design.md`

## Global Constraints

- Subtitle formats stay `.srt .ass .ssa .vtt`; each at most `subtitles.MaxSize` (2 MB) once unpacked; at most `downloads.MaxUploads` (30) per upload.
- A `.zip` file itself is at most 2 MB.
- Labels: at most 40 characters; none for a lone subtitle; fallback "1", "2", ….
- File name of a labelled subtitle: `<video stem>.<label>.<lang><ext>`.
- Messages in en and bg; keys `error.zip_bad`, `error.zip_empty`.

## Review Focus

- A zip entry bigger than 2 MB once unpacked (a zip bomb, or a header that lies about the size): the read limit, not the header, decides; `TestUnzipEntryTooBig` in Task 1.
- A zip with subtitles inside a folder (`Subs/Dune.srt`): base name used; test in Task 1.
- Uploading a single plain `.srt` after an earlier zip of two variants on a filed download: both labelled links go, the plain one stays; test in Task 3.
- Two variants whose names differ only by case or are identical after cleaning: labels fall back to numbers and file names don't collide; test in Task 1.
- A zip upload on the Add page for a series where the two variants are for the same episode: both kept, labelled; test in Task 3.

---

### Task 1: Unzip and labels in `subtitles`

**Files:**
- Create: `internal/subtitles/zip.go`, `internal/subtitles/zip_test.go`

**Interfaces:**
- Produces: `type Entry struct{ Name string; Data []byte }`; `func Unzip(data []byte) ([]Entry, error)`; errors `ErrZip`, `ErrNoSubtitles`, `ErrCount` (and existing `ErrSize` wrapped in `*EntryError{Name string; Err error}` so the caller can name the file); `const MaxEntries = 30`; `func Labels(names []string) []string`.

- [ ] **Step 1: Tests** in `zip_test.go`, building zips with `archive/zip` in memory:
  - `TestUnzip`: entries `Subs/Dune.BluRay.srt`, `Dune.WEB.srt`, `readme.txt`, `__MACOSX/._Dune.WEB.srt`, `.hidden.srt`, folder `Subs/` → returns `[{Dune.BluRay.srt}, {Dune.WEB.srt}]` with their data, in archive order.
  - `TestUnzipNoSubtitles`: only `readme.txt` → `ErrNoSubtitles`.
  - `TestUnzipBad`: `[]byte("not a zip")` → `ErrZip`.
  - `TestUnzipEntryTooBig`: one `.srt` of `MaxSize+1` bytes → `errors.Is(err, ErrSize)` and `EntryError.Name == "big.srt"`.
  - `TestUnzipTooMany`: 31 `.srt` entries → `ErrCount`.
  - `TestLabels`: table:
    - `["Dune.2021.1080p.BluRay.x264-SPARKS.srt", "Dune.2021.1080p.WEB-DL.DDP5.1.H.264-FLUX.srt"]` → `["BluRay x264-SPARKS", "WEB-DL DDP5 1 H 264-FLUX"]`
    - `["a.srt", "a.ass"]` → `["1", "2"]` (stems identical)
    - `["Movie.BG.srt", "movie.bg.srt"]` → `["1", "2"]`
    - one 60-character differing part → cut to 40 runes, trimmed
    - `["x/y.srt"]`-style names are already base names; single name → `[""]`.
- [ ] **Step 2:** `go test ./internal/subtitles/` fails (undefined).
- [ ] **Step 3: Implement.** `Unzip`: `zip.NewReader`; skip `FileInfo().IsDir()`, any path segment `__MACOSX`, base names starting with `.`, extensions not in `Exts`; read each kept entry through `io.LimitReader(rc, MaxSize+1)`; more than `MaxEntries` kept → `ErrCount`. `Labels`: words = stem split on `.`, `_`, ` `; drop the longest common leading and trailing word runs (case-insensitive); join with " "; drop `/\:*?"<>|` and control characters; cut to 40 runes and trim spaces/dots; if any label is empty or two are equal (case-insensitive) return "1".."n"; `len(names) < 2` returns `make([]string, len(names))`.
- [ ] **Step 4:** tests pass.
- [ ] **Step 5:** commit `feat(subtitles): read subtitle zips and label variants`.

### Task 2: Label in storage and placement

**Files:**
- Create: `internal/db/migrations/00006_subtitle_label.sql`
- Modify: `internal/db/subtitles.go` (`Subtitle.Label`, insert/select), `internal/library/subtitles.go` (`Subtitle.Label`, `PlaceSubtitles`), `internal/downloads/subtitles.go` (`librarySub` copies Label)
- Test: `internal/library/subtitles_test.go`, `internal/db/db_test.go`

**Interfaces:**
- Produces: `db.Subtitle.Label string`, `library.Subtitle.Label string`; `PlaceSubtitles` signature unchanged.

- [ ] **Step 1: Tests.**
  - `TestPlaceLabelled` (library): movie video `library/Movies/Dune (2021)/Dune (2021).mkv`, subs `{ID:"a",Lang:"bg",Ext:".srt",Name:"x.srt",Label:"BluRay"}`, `{ID:"b",…,Label:"WEB"}` → links to `…/Dune (2021).BluRay.bg.srt` and `…/Dune (2021).WEB.bg.srt`.
  - `TestPlaceUploadDisplacesTorrentSubOtherFormat` (library): links include torrent's `…/Dune (2021).bg.ass`; one upload `.srt` bg → output has no `.bg.ass` link and has `.bg.srt`.
  - `TestSubtitleLabelRoundTrip` (db): `AddSubtitle` with `Label:"WEB"` then `Subtitles` returns it.
- [ ] **Step 2:** they fail.
- [ ] **Step 3:** migration `ALTER TABLE download_subtitles ADD COLUMN label TEXT NOT NULL DEFAULT ''` (down drops it); db read/write; `PlaceSubtitles` dst = stem + (`"."+Label` if set) + `"."+Lang+Ext`; drop input links whose `To` is `stem+"."+lang+e` for any `e` in `subtitles.Exts` when an upload for that video and lang is placed (besides exact dst matches, as now).
- [ ] **Step 4:** `make test` passes.
- [ ] **Step 5:** commit `feat(subtitles): keep a label per uploaded subtitle`.

### Task 3: Batches in the downloads service

**Files:**
- Modify: `internal/downloads/subtitles.go`, `internal/handlers/deps.go` (`LibrarySubtitle(ctx, video string, kind model.Kind, ups []Upload) error`), `internal/handlers/fakes_test.go`
- Test: `internal/downloads/service_test.go`

**Interfaces:**
- Consumes: `subtitles.Labels`, `db.Subtitle.Label`, `library.Subtitle.Label`.
- Produces: `(*Service).LibrarySubtitle(ctx, video string, kind model.Kind, ups []Upload) error`.

- [ ] **Step 1: Tests.**
  - `TestAddKeepsVariants`: movie added with uploads `Dune.BluRay.srt`, `Dune.WEB.srt` (bg), filed → files `Dune (2021).BluRay.bg.srt` and `Dune (2021).WEB.bg.srt` exist, two subtitle rows.
  - `TestAddSeriesVariantsSameEpisode`: series, uploads `Show.S01E01.BluRay.srt`, `Show.S01E01.WEB.srt` → both labelled next to the S01E01 video.
  - `TestLibraryVariantsReplaceEarlier`: filed movie; `LibrarySubtitle` with two uploads → two labelled files; then one plain upload → both labelled files and their `download_links` rows gone, `Dune (2021).bg.srt` present; one subtitle row.
  - `TestLibraryVariantsHandPlaced`: video with no owner; two uploads → two labelled files written.
  - `TestRefileKeepsLabels`: after variants, `Edit` title → the labelled names follow the new folder.
- [ ] **Step 2:** they fail.
- [ ] **Step 3: Implement.** `keep` becomes `keepBatch(ctx, kind, hash, rows []db.Subtitle, data [][]byte, names []string) ([]db.Subtitle, error)`: group rows by (pinned `Video` or `subtitles.Target(kind, Name)`, `Lang`); groups of two or more get `subtitles.Labels(names)`; write all, then `forget` older rows (not in the batch) sharing a group with a new one. `placeNow` clears per (video stem, lang) group once: the plain `stem.lang.<ext>` files (as `clearPlace` does) and this download's recorded links matching `stem.*.lang.<ext>`, removing file and link row; then applies all new links. `LibrarySubtitle` takes `[]Upload` (all prepared first; at most `MaxUploads`), labels from upload names, owner → `keepBatch` + group clear + apply + `AddLinks`; no owner → clear plain names, write each to its (labelled) name.
- [ ] **Step 4:** `make test` passes.
- [ ] **Step 5:** commit `feat(downloads): keep subtitle variants from one upload side by side`.

### Task 4: Zips in the forms

**Files:**
- Modify: `internal/handlers/downloads.go` (`uploads`), `internal/handlers/subtitles.go` (`postSubtitles`), `internal/templates/pages/add.html` (both `accept`s, hint), `internal/i18n/en.json`, `internal/i18n/bg.json`
- Test: `internal/handlers/subtitles_test.go`, `internal/handlers/downloads_test.go`

**Interfaces:**
- Consumes: `subtitles.Unzip`, `subtitles.EntryError`, `LibrarySubtitle(..., []downloads.Upload)`.

- [ ] **Step 1: Tests.**
  - `TestAddZipSubtitles`: POST /add with a zip holding `a.S01E01.srt` and `b.S01E01.srt` → `fakeDownloads.added[0].Subtitles` has those two names.
  - `TestMovieZipSubtitles`: POST /subtitles/:id with a zip of two → `libSubs[0]` gets two uploads; redirect `?subs=1`.
  - `TestBadZip`: `bad.zip` with junk → redirect with `subs_error=error.zip_bad`; zip with only `readme.txt` → `error.zip_empty`; entry too big → `error.sub_size` naming the entry.
  - `TestUploadAcceptsZip`: the Add page and movie page file inputs' `accept` contain `.zip`.
- [ ] **Step 2:** they fail.
- [ ] **Step 3: Implement.** In `uploads`, names ending `.zip` (case-insensitive): size over `subtitles.MaxSize` → `error.sub_size`; `Unzip` errors map to `InvalidError` keys `error.zip_bad` (Arg zip name), `error.zip_empty` (Arg zip name), `error.sub_size` (Arg entry name), `error.sub_count`. `postSubtitles`: zero uploads → `error.sub_missing`. Messages: en `"%s can't be opened as a zip file."`, `"%s has no subtitle files in it (.srt, .ass, .ssa or .vtt)."`; bg equivalents. Add-page hint gains "A .zip with them works too."
- [ ] **Step 4:** `make check && make test` pass; `make integration` passes.
- [ ] **Step 5:** commit `feat(web): upload subtitles as a zip`.
