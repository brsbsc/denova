<p align="center">
  <img src="./web/public/favicon.svg" alt="Denova icon" width="76" height="76">
</p>

<p align="center">
  <strong>Denova is an integrated AI creative platform for novel writing and AI role-playing games, with built-in AI Agents, Skills, SubAgent collaboration, automation, image generation, and project version management</strong>
</p>

<p align="center">
  English | <a href="README.md">中文</a>
</p>

<p align="center">
  <a href="https://github.com/BRSBSC/denova/releases"><img alt="Release" src="https://img.shields.io/github/v/release/BRSBSC/denova?style=flat-square"></a>
  <a href="./LICENSE"><img alt="License" src="https://img.shields.io/github/license/alfredxw/denova?style=flat-square"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.26.9%2B-00ADD8?style=flat-square&logo=go&logoColor=white">
  <img alt="Node.js" src="https://img.shields.io/badge/Node.js-22.13%2B-5FA04E?style=flat-square&logo=nodedotjs&logoColor=white">
</p>

<p align="center">
  Current version: <strong>v0.6.3</strong> (2026-10-22) · Beta · <a href="https://github.com/BRSBSC/denova/releases">Download the latest release</a>
</p>

> [!NOTE]
> This repository is the Docker edition of [alfredxw/denova](https://github.com/alfredxw/denova). It only builds and publishes container images and does not accept issues or pull requests. Please report app issues and suggestions to the upstream repository.

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

Supports LAN access, responsive mobile layouts, and PWA apps.

<img src="./img/mobile.png" alt="Denova mobile layout" width="360">

</details>

## Why Denova

Denova brings novel writing, interactive stories, structured lore, AI Agents, image generation, automation, and local version management into one workspace. It is designed for creative projects whose settings and content need to evolve over time.

Start a new book from an idea, import an existing novel to continue writing, or use character cards and lore to build a branching text adventure. Agents can read project content, use tools, and edit files while important changes remain reviewable, undoable, and recoverable.

## Core Features

- **Novel writing**: Markdown document and source editing, multiple tabs, find and replace, outlines and chapter plans, progress tracking, inline comments, change review, and novel import.
- **Creative Agents**: choose Native, Codex, Claude Code, Grok Build, or Antigravity CLI to work with selections, project files, and lore, with multiple conversations and task pause/resume. Image, SubAgent, and Goal capabilities depend on the runtime; see [CLI runtimes](docs/cli-runtimes.md).
- **Interactive games**: advance stories through player input, with branches, storyline switching, action suggestions, character and world state, rule checks, story planning, and read-aloud. Stage scenes with Lore backgrounds and character sprites, with presentation saved along story branches.
- **Lore**: organize settings with custom categories, document-style indexes, @ references, and backlinks; find text mentions in project files and a selected story branch, provide grouped context to Writing and Game Agents, and manage multiple images, audio, and covers for each entry.
- **Creative Setups and Skills**: reuse narrative styles, image styles, rules, and other creative configurations; extend Agent workflows with Skills, including a built-in novel-writing Skill for premises, outlines, scenes, and revision.
- **Resource Center**: discover, preview, and selectively import Creative Setups, Skills, Lore, plugins, and games; combine resources for export, track sources, review updates, and restore backups.
- **Plugins and game extensions**: install and manage plugins and custom games. Plugins can provide Agent tools, direct commands, and independent panels; games can define their own interface, gameplay, and save format. The workbench supports creation, builds, isolated previews, and export.
- **Image creation**: generate book covers, chapter illustrations, game backgrounds, and character artwork, then preview results, associate them with Lore, and manage them in the app.
- **Versions and recovery**: save local versions, inspect changes, restore historical files, and review or undo Agent edits to the workspace.
- **Automation**: run scheduled review, continuation, and custom creative tasks.
- **Cross-platform experience**: Chinese and English UI, light and dark themes, Windows / macOS / Linux support, remote access, installation to a phone's home screen, and in-app updates from online releases or uploaded packages.

## Writing and Game

Writing and Game are peer top-level destinations in the workbench. Writing focuses on ideas, settings, outlines, chapters, and progress. Game focuses on player actions, story branches, character state, and storyline progression.

Lore, Creative Setups, Skills, and version management are shared between the two workflows. Chapter progress and game state remain separate so temporary state from one destination does not affect the other.

## Quick Start

### Docker (Recommended)

This fork publishes the `ghcr.io/brsbsc/denova` image for `linux/amd64` and `linux/arm64`. The `latest` tag follows the newest release; you can also pin a version such as `v0.6.3`. The image includes Bash, Git, curl, Python, and ripgrep, but no other language toolchains or external Agent CLIs.

The container needs a first-login account: `DENOVA_USERNAME` and `DENOVA_PASSWORD` (1–72 UTF-8 bytes). They only apply while the data directory has no `config.toml`; change the password in the app's settings afterwards.

**docker run**

```bash
docker run -d --name denova \
  -p 8080:8080 \
  -e DENOVA_USERNAME=admin \
  -e DENOVA_PASSWORD=replace-with-your-own-password \
  -v denova-data:/data \
  --restart unless-stopped \
  ghcr.io/brsbsc/denova:latest
```

**Docker Compose**

Put the account and image tag in `docker/.env` (ignored by Git):

```dotenv
DENOVA_USERNAME=admin
DENOVA_PASSWORD=replace-with-your-own-password
DENOVA_IMAGE_TAG=latest
```

Start it from the repository root:

```bash
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

Then open `http://localhost:8080` and sign in. The bundled [docker/compose.yml](docker/compose.yml) publishes the port on the host's `127.0.0.1` only; change the mapping to `8080:8080` for LAN access, and put an HTTPS reverse proxy in front for internet access.

**Data and permissions**

- All data lives in `/data`: `/data/.denova` holds app data (settings, projects, sessions) and `/data/log` holds logs. The default is the named volume `denova-data`.
- The container starts as root, changes ownership of only `/data/.denova` and `/data/log` to `10001:10001`, then drops privileges to run the app.
- When bind-mounting a host directory (for example `-v /path/to/denova:/data`), the file system must allow chown. For NFS / NAS directories that refuse chown, grant UID `10001` read/write access on the host first, then run with `--user 10001:10001` (Compose: `user: "10001:10001"`) to skip the permission setup.
- External Projects need their directories mounted separately; the container cannot see unmounted host files.
- When importing existing data, `config.toml` must enable `allow_lan_access` and contain `remote_access_username` and `remote_access_password_hash`, or the container refuses to start. Do not turn off LAN access inside the container, or the port becomes unreachable.

**Update, backup, and rollback**

```bash
docker compose --env-file docker/.env -f docker/compose.yml pull
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

- With `docker run`, run `docker pull ghcr.io/brsbsc/denova:latest`, remove the old container, and recreate it with the same arguments; the data volume is kept.
- Do not use the in-app updater to replace the program inside the container, and do not run `docker compose down -v`, which deletes the data volume.
- Back up the data volume before upgrading. To roll back, set `DENOVA_IMAGE_TAG` to the older version and recreate the container; rolling back the image does not roll back the data format.

See [Container publishing](docs/container-publishing.en.md) for how the image is built, synced with upstream releases, and verified.

### Install a Release

**Denova Desktop (recommended)**: install it and open the app.

| System | Download |
| --- | --- |
| macOS Apple Silicon | [Download notarized DMG](https://github.com/alfredxw/denova/releases/download/macos-notarized-v0.8.0/denova-desktop-v0.8.0-macos-arm64.dmg) |
| Windows x64 | [Download EXE installer](https://github.com/alfredxw/denova/releases/download/v0.8.0/denova-desktop-v0.8.0-windows-x64-setup.exe) |

macOS: open the DMG and drag Denova into Applications. Windows: run the installer. Desktop does not provide macOS Intel or Linux builds. See the [desktop build guide](docs/desktop-distribution.md) to package from source.

macOS requires version 13 or later. Windows 11 and most Windows 10 devices already have WebView2; only devices without it need to install the [Microsoft WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/#download).

Desktop v0.7.0 and v0.7.1 need one manual installation of this version to switch update feeds; v0.7.2 can update automatically. If an older version cannot start, install this version and select your existing data folder. WebUI v0.7.1 and later can still update automatically.

The macOS DMG linked above is signed with Apple Developer ID and notarized by Apple. The Windows installer is unsigned and may show a security prompt on first launch. Desktop auto-updates still verify their own package signatures.

<details>
<summary>Denova WebUI · Browser interface</summary>

Run the downloaded program on your computer or server, then open its address in a browser. WebUI is a downloadable application, not a hosted website.

| System | Download |
| --- | --- |
| macOS Apple Silicon | [tar.gz](https://github.com/alfredxw/denova/releases/download/v0.8.0/denova-webui-v0.8.0-macos-arm64.tar.gz) |
| macOS Intel | [tar.gz](https://github.com/alfredxw/denova/releases/download/v0.8.0/denova-webui-v0.8.0-macos-x64.tar.gz) |
| Windows x64 | [zip](https://github.com/alfredxw/denova/releases/download/v0.8.0/denova-webui-v0.8.0-windows-x64.zip) |
| Linux x64 | [tar.gz](https://github.com/alfredxw/denova/releases/download/v0.8.0/denova-webui-v0.8.0-linux-x64.tar.gz) |
| Linux ARM64 | [tar.gz](https://github.com/alfredxw/denova/releases/download/v0.8.0/denova-webui-v0.8.0-linux-arm64.tar.gz) |

On macOS and Linux, run the installer:

```bash
curl -fsSL https://raw.githubusercontent.com/BRSBSC/denova/v0.6.3/scripts/install.sh | sh
```

Run `denova` after installation. Windows users and anyone who prefers manual installation can download the archive for their platform from [GitHub Releases](https://github.com/BRSBSC/denova/releases); on Windows, run `denova.exe`.

</details>

For stable use, choose a [Release](https://github.com/alfredxw/denova/releases/latest). The `master` branch may contain unreleased changes.

### First Run

1. Follow the startup guide to add a language-model API key and model name.
2. Create or import a book, or open an existing project directory.
3. Start creating from Writing, or create an interactive storyline from Game. Configure an image model only when you need image generation.

### Run from Source

You need Go 1.26.9+, Node.js 22.13+, pnpm, ripgrep, and Bash. On Windows, run these commands from Git Bash or WSL.

```bash
git clone https://github.com/alfredxw/denova.git
cd denova
corepack enable
./scripts/bootstrap.sh
```

Default addresses:

- Frontend: `http://localhost:7788`
- Backend: `http://localhost:7789`

## Models and Configuration

The recommended setup is through Settings: add a provider connection, select or enter a model, and test the connection. One connection can be reused by multiple models. Language models support built-in providers and custom API-compatible endpoints. Image models support OpenAI, xAI/Grok, Volcengine Seedream, Google Gemini Image, ComfyUI Workflow, and custom endpoints.

For Google Gemini text models, select “Gemini native API” in the connection's protocol options to use native tool calls, image inputs and reasoning continuation. Existing OpenAI-compatible connections keep their protocol.

Optional decision models can be configured in Settings for Game stage media, Lore semantic classification, and automation semantic triggers, with language-model fallback for uncertain results or service failures.

## Remote Access and Phone Usage

Enable LAN access under **Settings → Access**, set a username and password, and restart. Other devices can sign in at the displayed address. The host can also create a one-use sign-in QR code and link that expire after 5 minutes. Scan with a phone on the same LAN, or copy the link to another device. The browser stays signed in for 30 days across refreshes and server restarts; sign out from Settings. Phone browsers can add Denova to the home screen.

When running from source, QR codes and connection links use the backend port to serve the `web/dist` build. Run `pnpm --dir web build` and restart the backend before first use or after frontend changes. The local Vite entry point remains available for development with hot reload.

Docker containers already have LAN access enabled and use the account set at first start; no need to enable it in Settings.

For public or domain-based deployments, put Denova behind an HTTPS reverse proxy such as Caddy or Nginx so login credentials are not transmitted in cleartext. A reverse proxy on the same machine as Denova must forward the original `Host` or pass the client address in `X-Forwarded-For` (for Nginx, `proxy_set_header Host $host;` and `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`). A proxy that forwards neither is indistinguishable from a local browser and skips the login.

## Usage statistics

Release builds explain usage statistics on first use. Collection is enabled by default and starts only after the notice is acknowledged. Turn it off anytime under **Settings → General**. PostHog receives fixed activity, feature usage, retention, version, platform, execution outcome, duration in milliseconds, error category, HTTP status, and theme/language changes, without creative text, prompts, AI replies, images, project names, file paths, keys or raw error text. A random local ID links return visits; the cloud receives the connection IP. Source and development builds do not send statistics. See the [metric and field definitions](./docs/analytics.md).

## Development

Common commands:

```bash
./scripts/bootstrap.sh fe
./scripts/bootstrap.sh be
./scripts/build.sh
```

## License

[Apache-2.0](./LICENSE)
