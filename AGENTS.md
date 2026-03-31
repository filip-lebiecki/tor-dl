# AGENTS.md

## Project Overview

Torrent Downloader web application. Single Go binary serving an embedded SPA frontend and a REST API. Uses `github.com/anacrolix/torrent` for BitTorrent protocol. Files are downloaded one at a time in queue order.

## Build & Run Commands

```bash
go build -o tor-dl .
./tor-dl                  # starts on http://localhost:8080
```

The binary embeds `static/` via `//go:embed`. Any changes to `static/index.html` require a rebuild.

## Lint / Vet / Test

```bash
go vet ./...              # static analysis (no separate linter configured)
go test ./...             # run all tests (none exist yet)
go test -run TestFoo ./...  # run a single test by name
go test -v ./...          # verbose output
```

No external linter (golangci-lint, etc.) is configured. Use `go vet` as the primary check.

## Codebase Layout

```
main.go           All Go code: structs, HTTP handlers, torrent logic, stats loop
static/index.html Single-page frontend (HTML + inline CSS + inline JS, no build step)
go.mod / go.sum   Module "tor-dl", Go 1.26.1
downloads/        Runtime download directory (created automatically, gitignored)
```

## Architecture

- **App struct** — holds `*torrent.Client`, a `sync.Mutex`-protected `[]*QueueItem` queue, and config.
- **QueueItem** — pairs a public JSON-safe view (`publicItem`) with private fields (`*torrent.Torrent`, speed tracking state).
- **Sequential downloading** — only one torrent is active ("downloading" or "fetching") at a time. When it completes or is removed, the next "waiting" item starts automatically via `startNextLocked()`.
- **Stats polling** — a background goroutine (`statsLoop`) ticks every 500ms, updates progress/speed on each active item, and transitions to "complete" when done.
- **Frontend** — polls `GET /api/queue` every 1 second. No WebSocket; no framework; vanilla JS.

## REST API

| Method | Path                | Description                              |
|--------|---------------------|------------------------------------------|
| POST   | `/api/add`          | Body: `{"magnet":"magnet:?xt=..."}`     |
| GET    | `/api/queue`        | Returns JSON array of queue items        |
| DELETE | `/api/remove/{id}`  | Remove by info-hash hex string           |

## Go Code Style

### Imports

Grouped with blank lines separating: stdlib, then third-party. Sorted alphabetically within each group. Use `goimports`-compatible ordering:

```go
import (
    "embed"
    "encoding/json"
    "fmt"
    // ... stdlib

    "github.com/anacrolix/torrent"
)
```

### Naming

- **Types**: `PascalCase` — `App`, `QueueItem`, `FileJSON`, `publicItem`.
- **Methods**: `PascalCase` receivers — `handleAdd`, `addMagnet`, `startItem`, `remove`.
- **JSON fields**: `camelCase` in tags — `json:"magnetURI"`, `json:"totalBytes"`.
- **Private struct fields**: short lowercase — `t`, `mu`, `prevRead`, `prevTime`.
- **Acronyms**: keep capitalized — `MagnetURI`, `ID`, `TotalBytes`, `DoneBytes`.

### Structs & Types

- Use `int64` for byte counts and sizes.
- Use `float64` for percentages.
- Use `time.Time` for timestamps.
- Separate public-facing DTOs (`publicItem`) from internal state structs (`QueueItem`).
- Embed `//go:embed static` for the frontend.

### Error Handling

- Return `error` from internal methods (`addMagnet`, `remove`).
- In HTTP handlers, map errors to status codes: `http.StatusBadRequest` for validation, `http.StatusInternalServerError` for internal errors, `http.StatusNotFound` for missing resources.
- Use `http.Error(w, msg, code)` for error responses.
- Use `log.Fatal` for startup failures only.

### Concurrency

- All queue mutations go through `app.mu.Lock()` / `app.mu.Unlock()`.
- `startItem` is called under the lock; the `GotInfo()` goroutine re-acquires the lock.
- `statsLoop` runs in a background goroutine with a 500ms ticker.
- Methods called under lock have `Locked` suffix: `startNextLocked()`.

### HTTP Handlers

- Follow the pattern: decode request body → validate → call internal method → encode JSON response.
- Always set `Content-Type: application/json` before encoding.
- Use Go 1.22+ method+pattern routing: `mux.HandleFunc("POST /api/add", ...)`.
- Use `r.PathValue("id")` for path parameters.

## Frontend Style (static/index.html)

- Single file, no build tools, no frameworks, no imports.
- CSS: minified single-line rules, CSS custom properties for theming, dark theme.
- JS: vanilla ES6+, arrow functions, template literals, async/await.
- State management: poll the API every 1 second, re-render the entire queue list via `innerHTML`.
- No comments in production CSS/JS — keep the file compact.

## Key Dependencies

- `github.com/anacrolix/torrent` v1.61.0 — BitTorrent client (Go)
- Go stdlib only for HTTP (`net/http`), JSON, embed, sync, time

## Torrent States

| State       | Meaning                                      |
|-------------|----------------------------------------------|
| `waiting`   | In queue, not yet started                    |
| `fetching`  | Waiting for torrent metadata from peers      |
| `downloading` | Got info, actively downloading pieces      |
| `complete`  | All pieces downloaded                        |
