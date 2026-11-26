# Upstream releases and GHCR images

This fork checks the latest stable `alfredxw/denova` release at minute 23 of every hour (UTC) through `.github/workflows/sync-upstream-release.yml`. Scheduled runs may be delayed. Only `vMAJOR.MINOR.PATCH` releases are accepted; prereleases are excluded. The workflow does not merge upstream branches.

## Enable publishing

1. Push these changes to the default `master` branch of `BRSBSC/denova`.
2. Enable workflows in the fork's **Actions** tab. If the initial push did not start a run, select **Sync upstream release and publish GHCR → Run workflow**.
3. Allow `packages: write` for the built-in `GITHUB_TOKEN`. No separate PAT or `contents: write` permission is needed, and no GitHub Releases are created or modified. Organization restrictions may require administrator changes. If the GHCR package already exists, grant this repository access under the package's Actions access settings.
4. After the first publication, check **Packages → denova → Package settings**. Set visibility to Public for anonymous pulls; a public source repository does not guarantee a public package.
5. Check the Actions logs and Summary for successful image publication. GitHub may disable scheduled workflows in inactive public repositories; re-enable them in Actions when needed.

Images are published as `ghcr.io/brsbsc/denova:v0.4.5` (example), supporting `linux/amd64` and `linux/arm64`. Upstream sync publishes versioned Docker images only and never creates GitHub Releases; `ghcr.io/brsbsc/denova:latest` is moved only by this fork's source releases, described below. The publishing job is restricted to `BRSBSC/denova`. Other forks must adjust the repository condition and Compose image address.

## Publication and retries

- Download only `checksums.txt` and the Linux bundles (`denova*-linux-*.tar.gz`) from the upstream release and verify every archive against `checksums.txt`. Missing Linux architectures, checksum failures, or unsafe archive paths stop publication.
- Package the released Linux bundles without recompiling the application. Preserve the frontend, Skills, updater, and license. Add Bash, Git, curl, Python, and ripgrep. The image stays lightweight without preinstalling external Agent CLIs (Antigravity CLI, Claude Code, Codex), which can be installed on demand into the data volume from web settings; additional language toolchains require an extended image.
- Application source fixes must first be included in the Linux bundle used by the image. Pushing an application patch to this fork or forcing a rebuild of the original version does not compile that patch into the image. Sync an upstream release containing the fix, or separately build and verify a patched bundle and image.
- Build and test both architectures: version, HTTP frontend, rejection of unauthenticated access, login, settings access, `/data/.local/bin` permissions, and configuration/session persistence after restart (fork source builds also assert managed external Agent CLI and web OAuth availability). Push the version image only after these tests pass, attaching `io.denova.upstream.mirror` and `io.denova.upstream.digest` OCI labels for idempotency.
- Synchronization checks GHCR OCI labels directly: if the target tag already exists without `io.denova.upstream.mirror` (meaning it was published from this fork's source, such as `v0.8.0`), mirroring always skips it. Mirroring never moves `latest` and never creates GitHub Releases.
- Scheduled runs process only the current latest release, not all historical releases. Enter a manual `tag` to backfill a version. Enable `force` to rebuild a mirrored upstream image. Pushing container or synchronization changes to `master` also checks and rebuilds if applicable.

The entire pipeline targets Docker images only, with no desktop installers and no GitHub Releases.

## Run the container

Create `docker/.env` (ignored by Git) with initial credentials:

```dotenv
DENOVA_USERNAME=admin
DENOVA_PASSWORD=replace-with-your-own-password
DENOVA_IMAGE_TAG=latest
```

Passwords must contain 1–72 UTF-8 bytes. From the repository root, in either PowerShell or Bash:

```sh
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

Open `http://localhost:8080` and sign in. Compose publishes only on the host loopback address by default. Use `8080:8080` for LAN access, and an HTTPS reverse proxy for internet access.

The container prepares volume permissions at startup, then runs the application as UID/GID `10001:10001`. The `denova-data` named volume persists `/data`, including application data in `/data/.denova`, logs in `/data/log`, on-demand managed external Agent CLI binaries in `/data/.local/bin`, and CLI credentials in `/data/.gemini`, `/data/.claude`, and `/data/.codex` (plus `/data/.local/share/keyrings` when present). Initialization rejects symbolic links at these managed paths, ensures `/data/.local/bin` exists, and adjusts ownership only inside managed directories while preserving file contents and permission bits; it does not recursively change other user directories under `/data` or follow symbolic links. `DENOVA_DIR` must remain inside `/data`. Host bind mounts must allow ownership initialization. Read-only mounts or NFS/NAS filesystems restricting chown require host-side permissions for that UID and Compose `user: "10001:10001"` to skip ownership preparation. External Projects require additional mounts; the named volume does not include arbitrary host files.

The image enables container-adapted external runtime sandboxing (`DENOVA_CONTAINER_SANDBOX=1`) and the managed CLI directory (`DENOVA_MANAGED_CLI_DIR=/data/.local/bin`) by default, allowing Antigravity CLI (`agy`), Claude Code (`claude`), and Codex (`codex`) to run under standard `docker compose up` (non-root UID `10001`, default seccomp, no `--privileged`). Under **Settings → Agents → Runtime**, you can install verified official binaries on demand into `/data/.local/bin`, check for and apply updates, and sign in or out via unified web OAuth (with credentials persisted under `/data` across container recreations), or fall back to built-in terminal presets (`codex`, `claude`, `agy`). Background automatic updates apply only to already-installed managed CLIs and can be toggled under **Settings → Updates**; the CLIs' own background auto-updaters are disabled (`AGY_CLI_DISABLE_AUTO_UPDATE=true`, `DISABLE_AUTOUPDATER=1`) to avoid conflicts with Denova's managed updater.

The first startup writes network settings and a bcrypt password hash only when no user `config.toml` exists. Existing settings are never replaced. Environment credentials initialize a new volume only; change subsequent passwords through application settings. Imported configurations must enable `allow_lan_access` and provide `remote_access_username` and `remote_access_password_hash`. Disabling LAN access in the application makes the container port unreachable.

Upgrade by replacing the container while keeping its data volume. Do not use the in-app updater to modify image binaries. Do not run `down -v`, which deletes the volume.

```sh
docker compose --env-file docker/.env -f docker/compose.yml pull
docker compose --env-file docker/.env -f docker/compose.yml up -d
```

To roll back, select an earlier `DENOVA_IMAGE_TAG` and recreate the container. Back up the data volume before upgrades: rolling back an image does not reverse data migrations.

## Source fixes published by this fork

The `Release` workflow builds source from each `v*` tag rather than using upstream bundles. It verifies full Linux CI for the same commit, packages only the `linux-amd64` and `linux-arm64` bundles needed for the container, builds and smoke-tests both Docker architectures, and pushes the version tag and `latest` (without building Windows/macOS installers or desktop packages). The image revision label records the actual source commit.

The `v0.8.0fix2` image is `ghcr.io/brsbsc/denova:v0.8.0fix2`. Set `DENOVA_IMAGE_TAG=v0.8.0fix2` in `docker/.env` and run the update commands above. `latest` always references the most recent source release and is unaffected by mirroring. To run an unmodified upstream build, set `DENOVA_IMAGE_TAG` to the upstream version (for example `v0.5.1`). A version this fork releases itself (such as `v0.8.0`) is always this fork's source build; mirroring skips that upstream version.

Mirroring continues to process upstream stable releases and publishes the version image of a newer upstream version later, without changing `latest` or creating GitHub Releases.

## Validation

```sh
python -B -m unittest discover -s scripts -p test_sync_upstream_release.py
python -B -m unittest discover -s docker -p test_entrypoint.py
bash -n docker/smoke-test.sh
sh -n docker/prepare-data.sh
```

The publishing workflow performs actual builds and smoke tests with isolated volumes for both architectures. It does not use development services, real model credentials, or user data. Pull requests run script tests and syntax checks only; they do not publish images.

References: [GitHub Container registry permissions and visibility](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry), [Docker multi-platform builds](https://docs.docker.com/build/building/multi-platform/), [GitHub scheduled events](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule).
