# Torrent Downloader

A self-hosted web interface for downloading torrents via magnet links. Built with Go and a single-page frontend — no Node.js, no build tools, just one binary.

![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)

## Features

- Paste magnet links and download one at a time in queue order
- Real-time progress bars with download speed
- Remove items from the queue (auto-starts the next one)
- Browse and download completed files from the web UI
- Single binary, no external dependencies

## Quick Start

```bash
go build -o tor-dl .
./tor-dl
```

Open [http://localhost:8080](http://localhost:8080).

Downloaded files are saved to `./downloads/`.

## Options

```
./tor-dl [--port PORT]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8080` | Port to listen on |

Example:

```bash
./tor-dl --port 9090
```

## API

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/add` | Add a magnet link — body: `{"magnet":"magnet:?xt=..."}` |
| `GET` | `/api/queue` | Get all queue items with progress/speed |
| `DELETE` | `/api/remove/{id}` | Remove a torrent by its info hash |
| `GET` | `/downloads/` | Browse completed files |

## Tech Stack

- **Backend:** Go, [anacrolix/torrent](https://github.com/anacrolix/torrent)
- **Frontend:** Vanilla HTML/CSS/JS (embedded in binary)
- **Protocol:** BitTorrent via magnet links, DHT, PEX

## License

MIT
