# Shortcuts for developing the media app.

.PHONY: test check integration

# test runs the Go tests against a throwaway Postgres (started with --rm,
# so stopping it removes it).
TEST_DB := media-test
test:
	@docker stop $(TEST_DB) >/dev/null 2>&1 || true
	@docker run -d --rm --name $(TEST_DB) -e POSTGRES_PASSWORD=test -p 127.0.0.1:55434:5432 postgres:16-alpine >/dev/null
	@trap 'docker stop $(TEST_DB) >/dev/null' EXIT; \
	for i in $$(seq 30); do docker exec $(TEST_DB) pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1 && break; sleep 1; done; \
	TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55434/postgres?sslmode=disable' go test -race -count=1 ./...

# check runs gofmt and go vet.
check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt on the files above"; exit 1; }
	go vet ./...

# integration runs the app against the real Jellyfin and Transmission
# images (scripts/integration.sh; needs Docker, takes a few minutes).
integration:
	scripts/integration.sh
