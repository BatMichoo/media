# --- Build ---
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/media ./cmd/media

# --- Run ---
FROM alpine:3.20
COPY --from=builder /out/media /usr/local/bin/media
# Templates, translations, theme and player files and migrations are built
# into the binary, so nothing else is copied. The user is set by compose
# (the host user, so files in the media folder belong to them).
EXPOSE 8080
CMD ["media"]
