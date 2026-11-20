# Subtitle RAR archives: design

Follows `2026-11-20-subtitle-zip-design.md`, which left `.rar` out because
Go can't read it on its own. Bulgarian subtitle sites often hand out
`.rar`, so both upload forms now take it too.

## Approach

`github.com/nwaples/rardecode/v2` (pure Go, so the `CGO_ENABLED=0` image
build is unchanged; reads RAR4 and RAR5). `subtitles.Unzip` became
`subtitles.Unpack(name, data, max)`, which picks zip or RAR by the file's
extension and runs every entry through the same steps as before: folders,
`__MACOSX`, hidden files, links and non-subtitle files skipped; each file
read through the 2 MB limit; at most `max` subtitles, shared across one
upload; names that aren't UTF-8 read as cp866.

## Decisions

- The RAR decoder may ask for at most a 64 MB dictionary (WinRAR's
  defaults are 32 MB for RAR5 and 4 MB for RAR4), so a crafted archive
  can't make it reserve gigabytes.
- An archive split into parts (`.part1.rar`) or with a password is
  refused. The reader can't tell a split archive from a cut-off one (both
  end early), so there's one message for all three, which names split and
  password-protected archives; nothing is ever taken from such an archive
  in part.
- The zip messages became archive ones: `error.archive_bad`,
  `error.archive_empty`.
- Go has no RAR writer, so tests build RAR5 archives (stored files) with
  `internal/subtitles/rartest`, written from the format's description and
  checked with `unrar t` (`RAR_CHECK=<dir> go test -run TestRarOfIsReal
  ./internal/subtitles/`, then `unrar t <dir>/check.rar`). Compressed data
  is the library's own concern.
