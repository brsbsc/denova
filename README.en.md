<p align="center">
  <img src="./web/public/favicon.svg" alt="Denova icon" width="76" height="76">
</p>

<p align="center">
  <strong>Denova Docker Edition — Containerized AI creative platform for novel writing and AI role-playing games</strong>
</p>

<p align="center">
  English | <a href="README.md">中文</a>
</p>

<p align="center">
  <a href="https://github.com/brsbsc/denova/pkgs/container/denova"><img alt="GHCR" src="https://img.shields.io/badge/GHCR-ghcr.io%2Fbrsbsc%2Fdenova-2496ED?style=flat-square&logo=docker&logoColor=white"></a>
  <img alt="Arch" src="https://img.shields.io/badge/Arch-linux%2Famd64%20%7C%20linux%2Farm64-0db7ed?style=flat-square">
  <a href="./LICENSE"><img alt="License" src="https://img.shields.io/github/license/alfredxw/denova?style=flat-square"></a>
</p>

<p align="center">
  Current image baseline: <strong>v0.8.0fix1</strong> · Docker only · <a href="https://github.com/brsbsc/denova/pkgs/container/denova">View GHCR Package</a>
</p>

> [!NOTE]
> This repository is the **Docker-only** edition of [alfredxw/denova](https://github.com/alfredxw/denova). It exclusively builds and publishes `ghcr.io/brsbsc/denova` multi-arch container images (`linux/amd64`, `linux/arm64`) with no Windows/macOS installers or GitHub Releases, and does not accept issues or pull requests. For desktop builds, native installers, or app feedback, please visit the upstream repository [alfredxw/denova](https://github.com/alfredxw/denova).

![Denova Writing](./img/ide.png)

<details>
<summary>View more screenshots</summary>

### Game

![Denova Game](./img/interactive.png)

### Lore Library

![Denova Lore Library](./img/setting.png)

### Creative Setups

![Denova Creative Setups](./img/story-teller.png)

### Change review

![Denova change review](./img/review.png)

### Workspace

![Denova workspace](./img/workspace.png)

Supports LAN/remote access, responsive mobile layouts, and PWA home-screen installation.

<img src="./img/mobile.png" alt="Denova mobile layout" width="360">

</details>

## Docker Edition Highlights

- **Multi-arch GHCR images**: `ghcr.io/brsbsc/denova:latest` (and pinned version tags such as `v0.8.0fix1`) built for `linux/amd64` and `linux/arm64`.
- **Non-root runtime**: The entrypoint prepares ownership on managed data directories and drops privileges to `UID/GID 10001:10001`, with remote-access authentication enabled out of the box.
- **Managed External Agent CLIs & Web OAuth in Containers**:
  - The base image stays lightweight (Bash, Git, curl, Python, ripgrep) without preinstalling heavy external Agent CLIs.
  - Enables container sandbox adaptation (`DENOVA_CONTAINER_SANDBOX=1`) by default so **Antigravity CLI (`agy`)**, **Claude Code (`claude`)**, and **Codex (`codex`)** run under standard unprivileged containers (no `--privileged`).
  - Install, verify, and update external CLIs on demand into the persistent `/data/.local/bin` volume directly from **Settings → Agents → Runtime**, and sign in/out via unified **Web OAuth** (credentials persist under `/data` across container recreations), or use built-in terminal presets (`agy`, `claude`, `codex`).
  - Optional background auto-updates (every 24h) for already-installed CLIs can be toggled under **Settings → Updates**.
- **Privacy-first defaults**: Usage statistics are off by default with no first-run prompt, and can be toggled anytime under **Settings → General** (see [analytics documentation](./docs/analytics.md)).

## Quick Start

Set an initial account via `DENOVA_USERNAME` and `DENOVA_PASSWORD` (1–72 UTF-8 bytes). These variables are only used when no `config.toml` exists in the data volume yet; change the password inside the web UI afterwards.

### Option 1: Docker Compose (Recommended)

Create `docker/.env` at the repository root (ignored by Git):

```dotenv
DENOVA_USERNAME=admin
DENOVA_PASSWORD=replace-with-your-own-password
DENOVA_IMAGE_TAG=latest
```

Start with the bundled [docker/compose.yml](docker/compose.yml):

```bash
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

> [docker/compose.yml](docker/compose.yml) binds to `127.0.0.1:8080` on the host by default. Change the mapping to `8080:8080` for direct LAN access, or place an HTTPS reverse proxy in front for internet access.

### Option 2: docker run

```bash
docker run -d --name denova \
  -p 8080:8080 \
  -e DENOVA_USERNAME=admin \
  -e DENOVA_PASSWORD=replace-with-your-own-password \
  -v denova-data:/data \
  --restart unless-stopped \
  ghcr.io/brsbsc/denova:latest
```

Open `http://localhost:8080` and sign in with the configured credentials.

### First Run

1. Follow the startup guide to add a language-model provider connection, API key, and model name (one connection can be reused across models; Google Gemini supports the native Gemini protocol).
2. If you use external CLI engines (Antigravity CLI / Claude Code / Codex), open **Settings → Agents → Runtime** to install on demand and sign in via Web OAuth.
3. Create or import a project, start writing in **Writing** or launch an interactive storyline in **Game**, and configure an image model when needed.

## Environment Variables & Data Volume

### Common Environment Variables

| Variable | Description | Default |
| --- | --- | --- |
| `DENOVA_USERNAME` | Initial login username (ignored once `config.toml` exists) | Required on first run |
| `DENOVA_PASSWORD` | Initial login password, 1–72 UTF-8 bytes (ignored once `config.toml` exists) | Required on first run |
| `DENOVA_IMAGE_TAG` | Image tag used by Compose (e.g. `latest`, `v0.8.0fix1`) | `latest` |
| `DENOVA_CONTAINER_SANDBOX` | Container unprivileged sandbox adaptation | `1` (image default) |
| `DENOVA_MANAGED_CLI_DIR` | Persistent directory for on-demand managed Agent CLIs | `/data/.local/bin` (image default) |

### Data Layout & Permissions

All persistent state lives under `/data` (default named volume `denova-data`):

- `/data/.denova`: application configuration (`config.toml`), projects, sessions, and version history
- `/data/log`: application logs
- `/data/.local/bin`: on-demand managed external Agent CLI binaries installed via web settings
- `/data/.gemini`, `/data/.claude` (and `/data/.claude.json`), `/data/.codex`: persisted OAuth credentials and CLI settings

Permission & mount notes:

- The container starts as root solely to adjust ownership on the managed directories above to `10001:10001` (without touching unrelated user directories under `/data` or following symlinks), then drops privileges to `10001:10001`.
- Bind mounts (e.g. `-v /path/to/denova:/data`) require `chown` support; on NFS/NAS mounts that disallow `chown`, grant UID `10001` read/write access on the host and run with `--user 10001:10001` (Compose: `user: "10001:10001"`) to skip ownership preparation.
- External project directories must be mounted explicitly into the container (e.g. `-v /host/projects:/projects`).
- Imported `config.toml` files must keep `allow_lan_access = true` with `remote_access_username` and `remote_access_password_hash` configured; disabling LAN access inside the container makes the port unreachable.

## Reverse Proxy & HTTPS

For domain or public deployments, put Denova behind an HTTPS reverse proxy (Caddy, Nginx, etc.) so credentials are never sent in cleartext.

When the proxy runs on the same host as Denova, it **must forward the original `Host` or `X-Forwarded-For`**; otherwise Denova treats proxied traffic as local loopback and bypasses authentication. Minimal Nginx example:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

## Upgrade, Rollback & Backup

**Docker Compose:**

```bash
docker compose --env-file docker/.env -f docker/compose.yml pull
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

**docker run:**

```bash
docker pull ghcr.io/brsbsc/denova:latest
docker rm -f denova
# Recreate the container with the same -v denova-data:/data mount
```

- Back up the `denova-data` volume before upgrading.
- Do **not** use the in-app updater inside the container, and do **not** run `docker compose down -v` (which deletes the data volume).
- To roll back, set `DENOVA_IMAGE_TAG` back to an older tag and recreate the container (note that rolling back the image does not downgrade migrated data formats).

## Core Denova Capabilities

- **Novel writing**: Markdown & source editing, multi-tab workspace, find/replace, outlines and chapter plans, progress tracking, inline comments, change review, and novel import.
- **Creative Agents**: Native, Codex, Claude Code, Grok Build, and Antigravity CLI runtimes with multi-session pause/resume and project/lore context. See [CLI runtimes](docs/cli-runtimes.md).
- **Interactive games**: Branching storylines, action suggestions, character/world state, rule checks, story planning, read-aloud, and lore-backed stage presentation.
- **Lore library**: Custom categories, document-style indexes, `@` references and backlinks, mention search, and multi-image/audio/cover management.
- **Creative Setups, Skills & Resource Center**: Reusable styles and rules, Skill-extensible workflows, selective import/export, update review, and backups.
- **Plugins, Image Creation, Versions & Automation**: Plugin & custom game extensions, multi-provider image generation, local version diffs/recovery, and scheduled automation.

## Container Build & Publishing

For multi-arch image publishing, CI verification, upstream image sync, and local build instructions, see [Container publishing](docs/container-publishing.en.md).

## License

[Apache-2.0](./LICENSE)
