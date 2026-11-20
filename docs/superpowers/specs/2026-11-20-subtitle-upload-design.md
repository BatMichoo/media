# Subtitle upload: design

## Why

Downloads often come without Bulgarian subtitles. The person finds them on
a subtitle site, downloads a single file (one per movie or per episode;
zips with many files are rare), and wants the player to show them in
Bulgarian without renaming files by hand.

## Goals

- Upload subtitle files at two points:
  1. **On the Add page**, while starting a download. The files are held
     until the download is filed into the library.
  2. **On a library page**: a movie page (one upload) or a show page (one
     upload per episode).
- Each upload is saved next to its video under the video's name plus a
  language code, so Jellyfin lists it with the right language. For
  example, `Dune (2021).mkv` gets `Dune (2021).bg.srt`.
- Subtitles saved in Windows-1251 (common for Bulgarian files) are
  converted to UTF-8.
- Bulgarian is the default subtitle language for anyone who hasn't chosen
  one yet. Choosing a subtitle in the player's ⚙ menu remains the way to
  change it. There is no separate settings page.

## Non-goals

- Zip or other archives.
- Picture subtitle formats (`.sub`/`.idx`, `.sup`). Only text formats:
  `.srt`, `.ass`, `.ssa`, `.vtt`.
- Searching or downloading subtitles from sites.
- Editing or timing subtitles.
- A settings page.

## Anti-goals

- An uploaded subtitle disappears when a download is re-filed (Edit) or
  Jellyfin rescans.
- A subtitle gets attached to the wrong episode by guessing.
- Removing a download leaves stray subtitle files or empty folders behind.

## Constraints

- The library is built from hard links under `DATA_DIR/library/…`, made by
  `library.Plan`/`library.Apply` and recorded per download in
  `download_links`. `library.Undo` removes the recorded links and any
  folders left empty. Edit undoes all of a download's links and files it
  again.
- `DATA_DIR` is one filesystem with hard links (checked at start by
  `library.CheckLinkable`).
- Jellyfin reports an item's `Path` from inside its container. The part
  from `/library/` on matches the app's `DATA_DIR/library/…` (already
  relied on by `Item.ShowTitle`).
- Jellyfin reads the language from the filename: `.bg` becomes `bul` and
  `.en` becomes `eng`. The player's language preference stores these
  three-letter codes (`watching.PickTracks` compares them with
  `Track.Lang`).
- Migrations are numbered goose files in `internal/db/migrations`
  (currently up to `00002`).

## Approaches

- **A. Hold uploads in the app's own folder and place them when filing
  (chosen).** Uploads are stored once under
  `DATA_DIR/subtitles/<hash>/` and recorded in the database. Placing
  hard-links them next to the matching video and records the links like
  any other library link. Filing, re-filing (Edit) and removal all reuse
  the existing link bookkeeping. Library uploads use the same placement
  step immediately. Chosen because it covers both upload points with one
  code path and survives Edit.
- B. Drop uploads into the torrent's download folder and let the existing
  "subtitles that came with the torrent" matching place them. Rejected:
  that matching works by shared filename prefix, which uploads don't
  have, and it writes into a folder Transmission owns.
- C. Library uploads only. Rejected: the person wants to attach subtitles
  when starting the download, not come back later.

## Design

### Storage

- **New table** in migration `00003_download_subtitles.sql`. The
  columns are given under "Conflicts, re-filing and episodes without
  codes" below.
- **The stored file** lives at `DATA_DIR/subtitles/<hash>/<id><ext>`,
  already converted to UTF-8. This is the master copy. Library files are
  hard links to it.
- **Replacing:** uploading the same `(hash, lang, target)` again replaces
  the earlier row and stored file. The newest upload wins, so a wrong
  subtitle can be fixed by uploading again.

### Placement

`library.PlaceSubtitles(links []Link, subs []Subtitle) []Link` is a pure
function. Given the video links a filing produced and the stored
subtitles, it returns extra links, each from
`subtitles/<hash>/<id><ext>` to `<video dir>/<video stem>.<lang><ext>`.

Matching rules:

- **Movie, target `""`:** the main video, i.e. the link into the movie
  folder whose name is the folder name, not `extras/`. For a multi-part
  movie this means part 1.
- **Movie, target `partN`:** the link whose stem ends in ` - partN`.
- **Series, target `SxxEyy`:** the video link whose filename contains that
  episode code. Matching is case-insensitive, accepts `S1E3` as well as
  `S01E03`, and uses the same parser as uploads. When two videos match,
  pick the shorter path. When none match, the subtitle is not placed.

Behaviour on each path:

- **Filing:** `downloads.Service.file` calls it after `library.Plan`, then
  passes all links together to `Apply` and `AddLinks`. Subtitles that
  can't be placed stay stored. The download's row reports them, e.g.
  "1 subtitle didn't match an episode" in `Reason`, without failing the
  download.
- **Re-filing (Edit):** works unchanged. `Undo` removes the library links,
  which include the subtitle links. The stored masters remain, and the
  next filing places them again.
- **Removing a download:** with "delete files", existing `Undo` removes
  the subtitle links and the app also deletes `DATA_DIR/subtitles/<hash>/`.
  Without "delete files", the stored subtitles stay with the library files
  (the row cascade deletes the records but not the files). See builder
  notes.

### Upload at Add time

- **Form:** the Add form gets an optional multi-file input
  `subtitles` (accept `.srt,.ass,.ssa,.vtt`) and a language select
  `subtitle_lang` (Bulgarian selected, English). A hint below says that
  for a show, each file must contain its episode number (S01E03).
- **Targets:** a movie upload targets `""`, or `partN` when its name has a
  part marker (the same `partMarker` regex the planner uses). A series
  upload targets the episode code parsed from its name. A series file
  with no episode code is rejected with a form error naming the file.
  Nothing is stored for a submit that fails.
- **Duplicate torrent:** when the torrent was already added, the uploads
  are still stored against the existing download. If it's already filed,
  they're placed right away (see library upload).

### Upload from the library

- **Movie page:** an "Add subtitles" form (file + language). For a
  multi-part movie it also has a part select (Part 1…N).
- **Show page:** a small "add subtitles" button on each episode row. It
  opens the same form for that episode, preferably as an inline
  `<details>` so it works without JavaScript.
- **POST `/subtitles/{itemId}`** (multipart: `file`, `lang`, optional
  `part`) does the following:
  1. Look up the item in Jellyfin. Map its `Path` to `DATA_DIR/library/…`
     by the part from `/library/` on. Reject an item that isn't a movie or
     episode, or whose path is not under the library.
  2. Find the download that owns this video:
     `SELECT hash FROM download_links WHERE path = $1`.
     - **Found:** store it as an upload of that download. The target is
       `""`/`partN` for a movie, or the episode code taken from the
       episode's video filename. Then hard-link it next to the video,
       record the link in `download_links`, and refresh the item.
     - **Not found** (a file put in the library by hand): write the
       converted file straight next to the video as
       `<stem>.<lang><ext>`, replacing any file of that name. It isn't
       tracked.
  3. Ask Jellyfin to refresh just this item:
     `POST /Items/{id}/Refresh?MetadataRefreshMode=Default`, falling back
     to the library refresh on error. Redirect back to the page with a
     flash ("Subtitles added").

### Validation and conversion

- **Allowed:** extensions `.srt`, `.ass`, `.ssa`, `.vtt`, at most 2 MB
  each and 30 files per Add submit.
- **Rejected:** empty files and files containing NUL bytes.
- **Encoding:** valid UTF-8 is kept, minus a leading BOM. Otherwise the
  file is decoded as Windows-1251 and written as UTF-8. Line endings are
  kept.
- **Language** must be `bg` or `en`. Any other value is rejected.

### Conflicts, re-filing and episodes without codes (settled after review)

- **An upload beats the torrent's own subtitle.** When the torrent brings
  a subtitle that would land on the same library path as an upload
  (`<stem>.bg.srt`), `PlaceSubtitles` drops that torrent link from the
  plan before `Apply` runs. It does this on every filing, so the person's
  explicit choice wins. *(My call.)*
- **Replacing from the library:** before placing the new file, the old
  upload's library link is removed with `library.Undo` and its
  `download_links` row is deleted, and the old master is deleted. *(My
  call.)*
- **The target is worked out when filing, not stored.** The `target`
  column is dropped. Each filing re-derives the target from the stored
  original `name` under the download's *current* kind. A Movie↔Series
  Edit therefore still places subtitles whenever the names allow it.
  Library uploads store a synthetic `name` that carries their target, see
  below. *(My call.)*
- **Episode codes:** the parser accepts `S01E03`, `s1e3` and `1x03`, with
  separators allowed around them.
- **Library upload to an episode whose filename has no recognised code**
  (e.g. `Season 1/03 - Title.mkv`): the upload is pinned to that video's
  library path. It is stored with a `video` column holding the video's
  library path. `PlaceSubtitles` places a row that has `video` set next
  to exactly that path when the plan produces it. If an Edit renamed the
  folder, the subtitle stays stored and is reported as not placed. Rows
  from a library upload of an episode that *has* a code store the
  synthetic name `S01E03` and no `video`, so they follow Edits. *(My
  call.)*
- **Not placed:** a subtitle that can't be placed is reported in the
  download's `Reason` (e.g. "2 subtitles didn't match a video"). The
  state stays "filed".

So the table is:

```sql
CREATE TABLE download_subtitles (
  hash  TEXT NOT NULL REFERENCES downloads ON DELETE CASCADE,
  id    TEXT NOT NULL,     -- random, also the stored file's name
  lang  TEXT NOT NULL,     -- "bg" or "en"
  ext   TEXT NOT NULL,     -- ".srt", ".ass", ".ssa", ".vtt"
  name  TEXT NOT NULL,     -- upload's original name, or a synthetic "S01E03" / "part2" / "movie"
  video TEXT NOT NULL DEFAULT '', -- pinned library video path, or ""
  added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (hash, id)
);
```

"Same `(hash, lang, target)` replaces" now means the same
`(hash, lang, derived target or video)` at the moment of upload.

### Default subtitle language

`watching.PickTracks` is called with the person's preferences. When
`SubtitleLang` is `""` (never chosen), treat it as `"bul"`, but the
default only picks **text** subtitles. A Bulgarian picture track (PGS
inside an `.mkv`) would make the server re-encode the video, so it is
used only when the person picks it in the ⚙ menu. An explicit `"bul"`
preference keeps today's behaviour, picture tracks included. A person who
chose "Off" keeps `"off"` and gets no subtitles. Nothing is written to
the database until they choose. *(My call.)*

### Texts

New i18n keys in `en.json` and `bg.json`: the subtitle form labels,
language names, the episode-number hint, errors (wrong type, too big,
no episode number in a name, item not in the library) and the success
flash.

## Left to the builder

- Exact markup and styling of the forms. They should match the existing
  sheet forms; mobile first.
- Whether stored subtitles are removed when a download is removed
  *without* "delete files". Recommended: keep them, the same as the
  library files they belong to.
- The Windows-1251 table: `golang.org/x/text/encoding/charmap` (a new
  direct dependency) or a 128-entry table in the code. The builder picks.
