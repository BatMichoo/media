# media

The home server's media app: browse films and series, play them in the
browser with resume across devices, and add torrents (magnet links or
`.torrent` files) that get filed into the library on their own.

It runs as a submodule of the home server (`github.com/BatMichoo/homeserver`),
which hosts it behind its login. This repo knows nothing about how it's
hosted: it reads its settings from the environment and trusts the
`X-User-Id` / `X-User-Name` headers the home server's proxy sets (it must
only be reachable through that proxy).

## How it works

- **Jellyfin** (hidden, one service account) reads the library, finds
  posters and descriptions, and converts video a browser can't play. The
  app sets it up on its own the first time (account, the Movies and Shows
  libraries, hardware conversion) and never shows Jellyfin to anyone.
- **Transmission** (hidden) downloads into `downloads/`.
- **The app** files finished downloads into `library/Movies/<Title> (<Year>)/`
  or `library/Shows/<Title>/` with hard links (no copies, the torrent keeps
  seeding), stops seeding 72 hours after a download finishes, keeps each
  person's place in every video, and passes all video and images through
  itself, adding Jellyfin's token on the server side. The browser never
  sees the token, and only Jellyfin's video and image addresses can be
  reached through the app.

## Settings

| Variable | Default | What it is |
|---|---|---|
| `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASS` | port 5432 | The app's Postgres database |
| `LAN_DOMAIN` | | e.g. `homeserver.lan`; cookies are set for it, POSTs must come from it |
| `HTTPS_PORT` | `443` | Where the home server serves HTTPS (for the login links) |
| `JELLYFIN_URL` | `http://jellyfin:8096` | |
| `JELLYFIN_USER`, `JELLYFIN_PASSWORD` | | The service account the app creates and uses |
| `JELLYFIN_HWACCEL` | `vaapi` | `vaapi`: convert on the Intel GPU (`/dev/dri`); `none`: on the CPU |
| `TRANSMISSION_URL` | `http://transmission:9091` | |
| `TRANSMISSION_USER`, `TRANSMISSION_PASSWORD` | | Transmission's RPC login |
| `DATA_DIR` | `/data` | Holds `downloads/` and `library/`; the same path in all three containers |
| `POLL_INTERVAL` | `30s` | How often Transmission is checked |
| `ADDR` | `:8080` | Where to listen |

`DATA_DIR` must be on one filesystem with hard links (ext4, btrfs, xfs; not
exFAT, FAT or NTFS). The app checks this at start and says so on the
downloads page if it can't link.

## Tests

```sh
make test         # Go tests against a throwaway Postgres
make check        # gofmt and go vet
make integration  # the app with the real Jellyfin and Transmission images
```

`make integration` makes a short test video, turns it into a local
torrent, adds it, and checks that it gets filed (as a hard link), shows up
in the library, plays directly and converted (HLS), that Jellyfin's token
never reaches the browser, that removing it cleans up, and that a restart
changes nothing. It takes a few minutes; `KEEP=1` leaves the stack running
when it fails.
