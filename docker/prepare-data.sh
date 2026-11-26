#!/bin/sh
set -eu

fix_ownership() {
    find "$1" -xdev \( ! -uid 10001 -o ! -gid 10001 \) \
        -exec chown --no-dereference 10001:10001 {} +
}

# Mounted volumes can retain root-owned private files from an earlier image.
# Prepare only Denova's managed data, logs, CLI binaries, and CLI credential
# directories, then drop privileges before reading settings or starting the
# application and its Agent child processes.
if [ "$(id -u)" = "0" ]; then
    for argument do
        case "${argument}" in
            --version|-version)
                exec gosu 10001:10001 python3 /usr/local/lib/denova-entrypoint.py "$@"
                ;;
        esac
    done

    requested_data_dir="${DENOVA_DIR:-/data/.denova}"
    data_dir="$(realpath -m -- "${requested_data_dir}")"
    if [ "$(realpath -ms -- "${requested_data_dir}")" != "${data_dir}" ]; then
        echo "Container data preparation failed: DENOVA_DIR must not contain symbolic links" >&2
        exit 1
    fi
    case "${data_dir}" in
        /data/*) ;;
        *)
            echo "Container data preparation failed: DENOVA_DIR must resolve inside /data" >&2
            exit 1
            ;;
    esac
    managed_cli_dir=""
    requested_cli_dir="${DENOVA_MANAGED_CLI_DIR-/data/.local/bin}"
    if [ -n "${requested_cli_dir}" ]; then
        managed_cli_dir="$(realpath -m -- "${requested_cli_dir}")"
        if [ "$(realpath -ms -- "${requested_cli_dir}")" != "${managed_cli_dir}" ]; then
            echo "Container data preparation failed: DENOVA_MANAGED_CLI_DIR must not contain symbolic links" >&2
            exit 1
        fi
        case "${managed_cli_dir}" in
            /data/*) ;;
            *)
                echo "Container data preparation failed: DENOVA_MANAGED_CLI_DIR must resolve inside /data" >&2
                exit 1
                ;;
        esac
    fi
    for managed_path in \
        /data/log \
        /data/.local \
        /data/.local/bin \
        /data/.local/share \
        /data/.local/share/keyrings \
        /data/.gemini \
        /data/.claude \
        /data/.claude.json \
        /data/.codex
    do
        if [ -L "${managed_path}" ]; then
            echo "Container data preparation failed: ${managed_path} must not be a symbolic link" >&2
            exit 1
        fi
    done
    mkdir -p -- "${data_dir}" /data/log
    if [ -n "${managed_cli_dir}" ]; then
        mkdir -p -- "${managed_cli_dir}"
    fi
    # Nested paths under /data also need traversable parent directories. Adjust
    # only the path itself, leaving sibling directories and their contents alone.
    for target_dir in "${data_dir}" ${managed_cli_dir:+"${managed_cli_dir}"}; do
        parent_dir="$(dirname -- "${target_dir}")"
        while [ "${parent_dir}" != "/data" ]; do
            chown --no-dereference 10001:10001 "${parent_dir}"
            parent_dir="$(dirname -- "${parent_dir}")"
        done
    done
    chown --no-dereference 10001:10001 /data
    if [ -e /data/.local ]; then
        chown --no-dereference 10001:10001 /data/.local
    fi
    if [ -e /data/.local/share ]; then
        chown --no-dereference 10001:10001 /data/.local/share
    fi
    if [ -e /data/.claude.json ]; then
        chown --no-dereference 10001:10001 /data/.claude.json
    fi
    fix_ownership "${data_dir}"
    fix_ownership /data/log
    if [ -n "${managed_cli_dir}" ]; then
        # Sweep stale partial downloads/staged binaries left behind by abrupt kills/OOM.
        find "${managed_cli_dir}" -maxdepth 1 -name '.denova-cli-*' -type f -delete
        fix_ownership "${managed_cli_dir}"
    elif [ -e /data/.local/bin ]; then
        fix_ownership /data/.local/bin
    fi
    for optional_dir in /data/.local/share/keyrings /data/.gemini /data/.claude /data/.codex; do
        if [ -e "${optional_dir}" ]; then
            fix_ownership "${optional_dir}"
        fi
    done
    echo "Prepared container data ownership for UID/GID 10001:10001"
    exec gosu 10001:10001 python3 /usr/local/lib/denova-entrypoint.py "$@"
fi

# An explicit non-root --user override requires a writable mounted volume.
exec python3 /usr/local/lib/denova-entrypoint.py "$@"
