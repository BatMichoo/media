#!/bin/sh
# Runs the media app against the real Jellyfin and Transmission
# (testdata/integration): makes a short test video with subtitles, turns
# it into a local torrent, adds it, checks it gets filed and plays (direct
# and converted), that Jellyfin's token never reaches the browser, and
# that removing it cleans up. Everything is removed on exit.
set -eu

cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
DATA="$tmp/data"
mkdir -p "$DATA/downloads" "$DATA/library/Movies" "$DATA/library/Shows"
MEDIA_UID=$(id -u)
MEDIA_GID=$(id -g)
PORT=${PORT:-18080}
export DATA MEDIA_UID MEDIA_GID PORT
out="$tmp/out" # every response the "browser" got, checked for the token
: >"$out"

compose() { docker compose -f testdata/integration/docker-compose.yaml "$@"; }
cleanup() {
	if [ -n "${KEEP:-}" ]; then echo "kept: $tmp"; return; fi
	compose down -v --rmi local >/dev/null 2>&1 || true
	# Jellyfin and Transmission may have written files as other users.
	docker run --rm -v "$tmp:/t" alpine:3.20 rm -rf /t/data >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

fail() {
	echo "FAIL: $*" >&2
	compose logs --tail 40 media >&2 || true
	exit 1
}

user=6f1c2d3e-0000-4000-8000-000000000001
app() {
	curl -sS --max-time 60 -H "X-User-Id: $user" -H "X-User-Name: Tester" -H "Origin: https://media.homeserver.lan" \
		"$@" | tee -a "$out"
}
url="http://127.0.0.1:$PORT"

# Wait until $1 succeeds (a command), up to $2 seconds.
wait_for() {
	i=0
	while ! eval "$1" >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -lt "$2" ] || return 1
		sleep 1
	done
}

compose up -d --build --wait >/dev/null 2>&1 || fail "the stack doesn't start"

# 1. A 20-second test video with an English subtitle file, in a torrent folder.
src="$DATA/downloads/Test.Movie.2024"
mkdir -p "$src"
compose run --rm --no-deps -T -v "$src:/out" --entrypoint /usr/lib/jellyfin-ffmpeg/ffmpeg jellyfin \
	-loglevel error -f lavfi -i testsrc=duration=20:size=640x360:rate=25 -f lavfi -i sine=duration=20 \
	-c:v libx264 -pix_fmt yuv420p -c:a aac -shortest -y /out/Test.Movie.2024.mkv ||
	fail "can't make the test video"
printf '1\n00:00:01,000 --> 00:00:05,000\nHello from the test\n' >"$src/Test.Movie.2024.en.srt"

# 2. A torrent of that folder (no trackers; the data is already here).
compose exec -T -u "$MEDIA_UID" transmission transmission-create -o /data/test.torrent /data/downloads/Test.Movie.2024 >/dev/null ||
	fail "can't make the torrent"

# 3. Add it through the app.
wait_for "curl -fs $url/healthz" 60 || fail "media isn't healthy"
code=$(app -o /dev/null -w '%{http_code}' -F kind=movie -F "title=Test Movie" -F year=2024 \
	-F "torrent=@$DATA/test.torrent" "$url/add")
[ "$code" = 303 ] || fail "add answered $code"
echo "added: ok"

# 4. Transmission checks the local data and the app files it.
filed() { app "$url/downloads/list" | grep -q 'state-filed'; }
if ! wait_for filed 120; then
	app "$url/downloads/list" >&2
	fail "not filed"
fi
lib="$DATA/library/Movies/Test Movie (2024)"
[ -f "$lib/Test Movie (2024).mkv" ] || fail "no movie in the library: $(ls -R "$DATA/library")"
[ -f "$lib/Test Movie (2024).en.srt" ] || fail "no subtitles in the library"
[ "$(stat -c %i "$lib/Test Movie (2024).mkv")" = "$(stat -c %i "$src/Test.Movie.2024.mkv")" ] ||
	fail "the library file is a copy, not a hard link"
echo "filed: ok"

# 5. Jellyfin was set up by the app and finds it.
movie_id() { app "$url/movies" | grep -o 'href="/movies/[0-9a-f]\{32\}"' | head -1 | grep -o '[0-9a-f]\{32\}'; }
if ! wait_for movie_id 120; then
	app "$url/movies" >&2
	fail "the movie doesn't show up in the library"
fi
id=$(movie_id)
app "$url/movies" | grep -q 'Test Movie (2024)' || fail "the movie's title isn't the one typed"
echo "library: ok"

# 6. Played as it is: a range request through /stream.
play() { app -H 'Content-Type: application/json' -d "$1" "$url/play/$id"; }
direct=$(play '{"caps":{"containers":["mkv","mp4"],"videoCodecs":["h264"],"audioCodecs":["aac"],"hls":false}}')
echo "$direct" | grep -q '"direct":true' || fail "not played directly: $direct"
stream=$(echo "$direct" | sed 's/^{"url":"\([^"]*\)".*/\1/' | sed 's/\\u0026/\&/g')
code=$(app -o /dev/null -w '%{http_code}' -H 'Range: bytes=0-99' "$url$stream")
[ "$code" = 206 ] || fail "range request answered $code"
echo "$direct" | grep -q '"lang":"eng"' || echo "$direct" | grep -q '"subtitles":\[{' || fail "no subtitle track: $direct"
sub=$(echo "$direct" | grep -o '/stream/Videos/[^"]*Stream.vtt' | head -1)
[ -n "$sub" ] || fail "no subtitle file: $direct"
app "$url$sub" | grep -q 'Hello from the test' || fail "subtitles don't come through"
echo "direct play: ok"

# 7. A browser that can't play it gets HLS, which plays through /stream.
hls=$(play '{"caps":{"containers":["webm"],"videoCodecs":["vp9"],"audioCodecs":["opus"],"hls":false}}')
echo "$hls" | grep -q '"direct":false' || fail "not converted: $hls"
master=$(echo "$hls" | sed 's/^{"url":"\([^"]*\)".*/\1/' | sed 's/\\u0026/\&/g')
playlist=$(app "$url$master")
echo "$playlist" | grep -q '#EXTM3U' || fail "no playlist: $playlist"
# Follow to the media playlist and fetch its first segment.
next=$(echo "$playlist" | grep -v '^#' | grep . | head -1)
case "$next" in /*) next_url="$url$next" ;; *) next_url="$url$(dirname "$master")/$next" ;; esac
media_pl=$(app "$next_url")
seg=$(echo "$media_pl" | grep -v '^#' | grep . | head -1)
case "$seg" in /*) seg_url="$url$seg" ;; *) seg_url="$(dirname "$next_url")/$seg" ;; esac
code=""
for _ in $(seq 60); do
	code=$(app -o /dev/null -w '%{http_code}' "$seg_url")
	[ "$code" = 200 ] && break
	sleep 1
done
[ "$code" = 200 ] || fail "HLS segment answered $code ($seg_url)"
ps=$(echo "$hls" | sed 's/.*"playSessionId":"\([^"]*\)".*/\1/')
ms=$(echo "$hls" | sed 's/.*"mediaSourceId":"\([^"]*\)".*/\1/')
code=$(app -o /dev/null -w '%{http_code}' -d "item=$id&part=0&position=5&durations=20&playSessionId=$ps&mediaSourceId=$ms&event=stop" "$url/progress")
[ "$code" = 204 ] || fail "progress answered $code"
app "$url/" | grep -q 'id="continue"' || fail "no continue row"
echo "converted play: ok"

# 8. Jellyfin's token never reached the "browser": no api key in any
#    response, and not the app's own token either.
if grep -qi 'api_key\|apikey' "$out"; then fail "a response carries an api key"; fi
echo "no token: ok"

# 9. Remove with files: library folder and download gone.
hash=$(app "$url/downloads/list" | grep -o 'downloads/[0-9a-f]\{40\}/edit' | head -1 | cut -d/ -f2)
[ -n "$hash" ] || fail "no download hash"
app -o /dev/null -d delete_files=on "$url/downloads/$hash/remove"
gone() { [ ! -e "$lib" ] && [ ! -e "$src/Test.Movie.2024.mkv" ]; }
wait_for gone 30 || fail "files left after remove: $(ls -R "$DATA")"
echo "remove: ok"

# 10. A restart: setup runs again and still finds two libraries and
#     hardware conversion on.
compose restart media >/dev/null
wait_for "curl -fs $url/healthz" 60 || fail "media doesn't come back"
setup_done() { [ "$(compose logs media | grep -c 'jellyfin setup: done')" -ge 2 ]; }
wait_for setup_done 60 || fail "setup didn't finish after restart"
login=$(compose exec -T jellyfin curl -fsS -H 'Content-Type: application/json' \
	-H 'Authorization: MediaBrowser Client="test", Device="t", DeviceId="t", Version="1"' \
	-d '{"Username":"media","Pw":"jf-test"}' http://localhost:8096/Users/AuthenticateByName)
tok=$(echo "$login" | sed 's/.*"AccessToken":"\([^"]*\)".*/\1/')
[ -n "$tok" ] || fail "can't log in to Jellyfin: $login"
if grep -q "$tok" "$out"; then fail "a Jellyfin token reached the browser"; fi
auth="Authorization: MediaBrowser Client=\"test\", Device=\"t\", DeviceId=\"t\", Version=\"1\", Token=\"$tok\""
libs=$(compose exec -T jellyfin curl -fsS -H "$auth" http://localhost:8096/Library/VirtualFolders)
[ "$(echo "$libs" | grep -o '"CollectionType"' | wc -l)" = 2 ] || fail "not two libraries: $libs"
enc=$(compose exec -T jellyfin curl -fsS -H "$auth" http://localhost:8096/System/Configuration/encoding)
echo "$enc" | grep -q '"HardwareAccelerationType":"none"' || fail "conversion setting not applied: $enc"
echo "restart: ok"

echo "integration: ok"
