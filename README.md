# media

The home server's media app: browse films and series, play them in the
browser with resume across devices, and add torrents that get filed into
the library on their own.

It runs as a submodule of the home server (`github.com/BatMichoo/homeserver`),
which hosts it behind its login. This repo knows nothing about how it's
hosted: it reads its settings from the environment and trusts the
`X-User-Id` / `X-User-Name` headers the home server's proxy sets.

## Tests

```sh
make test    # Go tests against a throwaway Postgres
make check   # gofmt and go vet
```
