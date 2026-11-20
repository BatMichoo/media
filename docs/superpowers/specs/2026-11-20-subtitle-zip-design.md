# Subtitle zips: design

## Why

Subtitle sites often hand out a `.zip` instead of a bare `.srt`. It usually
holds one or two subtitles for the same video, and two means timings for
different releases (BluRay vs WEB, 23.976 vs 25 fps). Today the person has
to unpack it and upload the files by hand. A zip with many episodes is rare.

## Goals

- Both upload forms (the Add page, and a movie's or episode's page) take a
  `.zip` as well as plain subtitle files. The subtitles inside are taken
  out and handled as if uploaded one by one: same checks, same
  Windows-1251 to UTF-8 conversion, same episode matching on the Add page.
- Two (or more) subtitles for the same video and language from one upload
  are **all kept**, each as its own track, so the player's ⚙ menu offers
  them and the person switches while watching (they only find out which
  one is in sync once it plays).
- Each such track carries a short label taken from its file name, so the
  menu tells them apart.

## Non-goals

- `.rar`, `.7z` or other archives (zip only; Go reads it on its own).
- Zips inside zips.
- Picking the right timing automatically.
- Removing a single variant later (a new upload replaces them all).

## Anti-goals

- A zip that unpacks to a huge size or many files (a zip bomb).
- Variants from one upload replacing each other.
- An older upload's tracks lingering after a new upload for the same video
  and language.

## Constraints

- Uploads go through `handlers.uploads()`, then `downloads.prepare`
  (`subtitles.Prepare`): `.srt/.ass/.ssa/.vtt`, 2 MB each, at most 30.
- The library-page form takes one file today (`len(ups) != 1` is an
  error) and `Downloads.LibrarySubtitle` takes one `Upload`.
- Today a later upload for the same video and language replaces the
  earlier one (`keep` drops the earlier row; `clearPlace` removes the files
  at `<video>.<lang>.<ext>`).
- Jellyfin 12 reads `<video>.<label>.<lang>.<ext>` as language `<lang>` with
  title `<label>` (checked against the integration image: `Spike
  (2024).BluRay x264-SPARKS.bg.srt` is `bul`, title `BluRay x264-SPARKS`,
  shown as "BluRay x264-SPARKS - Bulgarian - SUBRIP - External"). Dots in
  the label are kept as part of the title.

## Approaches

1. **Open the zip at upload and feed its subtitles into the existing path**
   (chosen). One new function in `subtitles` reads the zip; the handlers
   turn each entry into an `Upload`. Everything downstream already handles
   a list of uploads.
2. Open the zip in the downloads service. Spreads archive handling across
   two layers for no gain.
3. For two variants: pick one automatically, or ask which to keep. Rejected
   in conversation: the person can only tell which is in sync while
   watching, so both are kept as tracks.

## Design

- `subtitles.Unzip(data) ([]Entry, error)` with `archive/zip`: keeps
  entries whose extension is a subtitle one (by base name), skips folders,
  `__MACOSX/` and dot files. Limits: at most 30 subtitle entries
  (`ErrCount`), each at most 2 MB once unpacked, read through a limit, not
  trusting the header (`ErrSize`, with the entry's name). No subtitles in
  it: `ErrNoSubtitles`; not a readable zip: `ErrZip`.
- `handlers.uploads()`: a file whose name ends in `.zip` (any case) is
  unpacked into uploads named after the entries (base name). The zip
  itself is limited to 2 MB like a subtitle file. New messages:
  `error.zip_bad`, `error.zip_empty` (en and bg). Both forms' `accept`
  attributes and the Add page's hint mention `.zip`.
- Library-page form: one or more uploads (from a zip) are fine; none is
  `error.sub_missing`. `LibrarySubtitle` takes `[]Upload`, all for that
  video.
- Labels: within one upload, subtitles for the same video and language
  (two or more) each get a label from their file names: the stems split
  into words on `.`, `_`, space; the words all of them share at the start
  and the end are dropped; the rest joined with spaces, unsafe file name
  characters removed, at most 40 characters. Empty or repeated labels fall
  back to numbers ("1", "2"). A lone subtitle has no label (today's
  `<video>.bg.srt`).
- `download_subtitles.label` (migration 00006, default `''`) keeps the
  label so re-filing names the files the same. `library.Subtitle.Label`;
  `PlaceSubtitles` names a labelled one `<video stem>.<label>.<lang><ext>`.
- Replacing: a new upload drops the earlier uploads for the same video and
  language (all of them, labelled or not), but never the ones in the same
  upload. When the download is already in the library, the library links
  it made for those earlier uploads are removed too, and so are the plain
  `<video>.<lang>.<ext>` files (as today, so uploads win over the
  torrent's own subtitle). On a first filing, an upload for a video and
  language also displaces the torrent's own subtitle of that language in
  any text format.
- Known limit, my call: for a video put in the library by hand (no
  download made it), there is no record of earlier labelled files, so a new
  upload removes only the plain `<video>.<lang>.<ext>` ones; earlier
  labelled variants there stay until removed by hand.

## Left to the builder

Test layout, error wording in Bulgarian, the exact word splitting
details.
