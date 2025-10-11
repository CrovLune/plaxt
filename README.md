<p align="center">
  <img src="./static/img/plaxt-logo.png" width="120" alt="Plaxt logo" />
</p>
<p align="center">
  <strong>Plaxt</strong><br/>
  Sync Plex activity to Trakt with a guided, username-free experience.
</p>
<p align="center">
  <a href="https://dl.circleci.com/status-badge/redirect/gh/CrovLune/plaxt/tree/main"><img src="https://dl.circleci.com/status-badge/img/gh/CrovLune/plaxt/tree/main.svg?style=shield" alt="CircleCI" /></a>
  <img src="https://img.shields.io/docker/pulls/crovlune/plaxt.svg" alt="Docker Pulls" />
</p>

---

## What Is Plaxt?

Plaxt listens for Plex webhooks and scrobbles each play to Trakt. The latest iteration adds a modern UI, a streamlined manual-renewal workflow, and support for multiple storage backends—all while keeping deployment simple.

The project was originally created by [XanderStrike](https://github.com/XanderStrike). This fork continues the work with an updated front end and fresh container images. The in-tree Plex webhook parser (`/plexhooks`) was also imported from [XanderStrike/plexhooks](https://github.com/XanderStrike/plexhooks) so the original author retains credit for that foundational work.

### Highlights

- **Guided onboarding**: enter your Plex username once, authorise Trakt, copy your webhook—done.
- **Manual renewal without typing**: pick a user, reuse the existing webhook, let Plaxt fetch Trakt display names automatically.
- **2025 look and feel**: glassmorphism styling, status indicators, responsive layout.
- **Multiple stores**: disk (default), Redis, or PostgreSQL.
- **Zero telemetry**: Plaxt only stores the tokens/display names it needs for renewals.

---

## Quick Start (Docker)

Copy `.env.example` to `.env`, fill in your Trakt credentials, then launch Plaxt:

```bash
docker run -d \
  --name plaxt \
  --restart unless-stopped \
  -p 8000:8000 \
  -v $PWD/plaxt-data:/app/keystore \
  --env-file ./.env \
  crovlune/plaxt:latest
```

An example compose file is available at `docker-compose.example.yml`; copy or adapt it to suit your environment.

> The Dockerfile supports multi-architecture builds (amd64, arm64, arm/v7). When you publish with `docker buildx --platform ...`, a single `crovlune/plaxt:latest` tag works on homelab servers and Raspberry Pi devices alike.

### Trakt Application Setup

1. Visit [Trakt OAuth applications](https://trakt.tv/oauth/applications) and create a new app.
2. Register both redirect URIs:
   - `https://your-host/authorize`
   - `https://your-host/manual/authorize`
3. Copy the Client ID (`TRAKT_ID`) and Client Secret (`TRAKT_SECRET`).

Point Plex webhooks at the URL Plaxt shows after onboarding (e.g. `https://your-host/api?id=<plaxt-id>`).

---

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `TRAKT_ID` | ✅ | Trakt OAuth client ID. |
| `TRAKT_SECRET` | ✅ | Trakt OAuth client secret. |
| `ALLOWED_HOSTNAMES` | ✅ | Comma/space-separated hostnames Plaxt will serve. |
| `LISTEN` | 🅾️ | Listen address (default `0.0.0.0:8000`). |
| `POSTGRESQL_URL` | 🅾️ | Enables PostgreSQL storage when set. |
| `REDIS_URL` / `REDIS_URI` & `REDIS_PASSWORD` | 🅾️ | Enables Redis storage. |

Plaxt falls back to the on-disk store at `/app/keystore` if neither Redis nor PostgreSQL is configured.

### Example `.env`

The repository ships with `.env.example`; copy it to `.env` and tweak values to match your deployment.

```env
TRAKT_ID=your-trakt-client-id
TRAKT_SECRET=your-trakt-client-secret
ALLOWED_HOSTNAMES=plaxt.example.com
POSTGRESQL_URL=postgres://plaxt:secret@postgres:5432/plaxt?sslmode=disable
REDIS_URL=redis://redis:6379
# Optional: LISTEN=0.0.0.0:8000
```

---

## Running From Source

```bash
go mod download
TRAKT_ID=... TRAKT_SECRET=... ALLOWED_HOSTNAMES=localhost go run .
```

Browse to `http://localhost:8000` and follow the wizard.

### Storage Backends
- **Disk**: default; data stored under `/app/keystore`.
- **Redis**: set `REDIS_URL` (or `REDIS_URI` + `REDIS_PASSWORD`).
- **PostgreSQL**: set `POSTGRESQL_URL`. Plaxt will auto-create the `trakt_display_name` column.

---

## Operational Notes

- Manual renewal keeps the existing webhook URL and never asks for the Plex username.
- Plaxt attempts to fetch the Trakt display name after each OAuth success; if it fails you can enter it manually on the success screen.
- Tokens older than 23 hours are refreshed automatically during webhook handling.

---

## Developer Guide

### Local workflow

- Install Go 1.24+ (module-aware toolchain).
- Format code: `gofmt -w <files>` (or run `find . -name '*.go' -exec gofmt -w {} \;`).
- Run tests: `go test ./...`.
- Upgrade deps: `go get -u ./... && go mod tidy`.

Static assets build through a lightweight Go-based pipeline. Run `go run ./tools/assets` after changing files in `static/css` or `static/js`; the command writes hashed, minified bundles into `static/dist/manifest.json` for the server to consume.

### Building containers

Build multi-platform images with Docker Buildx so a single tag works on x86_64 servers and ARM homelabs:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64,linux/arm/v7 \
  -t crovlune/plaxt:latest \
  .
```

Add `--push` to publish to a registry or `--load` to import a single-architecture image into your local Docker daemon.

### Do I need multi-platform images?

Yes, if you intend to run Plaxt on different CPU architectures. Containerization isolates dependencies, not instruction sets—an amd64 image will not run on a Raspberry Pi unless the host uses emulation. Publishing a multi-architecture manifest ensures Docker selects the correct image for each device automatically.

### Contributing

We welcome issues and pull requests! Before opening a PR:

1. Format your Go code (`gofmt`).
2. Run the full test suite (`go test ./...`).
3. Include context in commit/PR descriptions (what changed, why, testing notes).

Ideas that are always helpful:
- Additional test coverage for storage backends and the OAuth/manual renewal flow.
- UI/UX improvements or accessibility fixes.
- Deployment docs for other environments (Kubernetes, Nomad, etc.).

---

## Thanks

- [XanderStrike](https://github.com/XanderStrike) for creating Plaxt and publishing the original Docker images.
- Everyone keeping Plex and Trakt in sync by filing bugs, sharing logs, and contributing improvements.

Happy syncing! 🎬→📚
