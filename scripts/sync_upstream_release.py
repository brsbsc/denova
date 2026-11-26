"""Prepare verified upstream Linux release bundles for Docker-only GHCR sync.

Checks GHCR OCI labels directly instead of creating or mirroring GitHub Releases.
GITHUB_TOKEN needs packages:write on the destination repository only.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
from urllib.parse import quote

UPSTREAM = "alfredxw/denova"
MIRROR_LABEL = "io.denova.upstream.mirror"
DIGEST_LABEL = "io.denova.upstream.digest"
INSPECT_LABELS_FORMAT = '{{json (index .Image "linux/amd64").Config.Labels}}'


def run(*args):
    return subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout.strip()


def api(endpoint):
    return json.loads(run("gh", "api", endpoint))


def find_release(repo, tag):
    try:
        return api(f"repos/{repo}/releases/tags/{quote(tag, safe='')}")
    except subprocess.CalledProcessError as error:
        if "(HTTP 404)" in error.stderr:
            return None
        raise


def validate_release(release):
    if release.get("draft") or release.get("prerelease"):
        raise ValueError("Only published stable releases are supported")
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", release["tag_name"]):
        raise ValueError("Expected a stable vMAJOR.MINOR.PATCH tag")


def release_digest(release):
    facts = {"id": release["id"], "tag": release["tag_name"], "assets": [
        {key: asset[key] for key in ("id", "name", "size", "updated_at")}
        for asset in sorted(release["assets"], key=lambda asset: asset["name"])
    ]}
    return hashlib.sha256(json.dumps(facts, sort_keys=True).encode()).hexdigest()


def inspect_image_labels(image_ref):
    try:
        output = run("docker", "buildx", "imagetools", "inspect", image_ref, "--format", INSPECT_LABELS_FORMAT)
    except subprocess.CalledProcessError as error:
        stderr = (error.stderr or "").lower()
        if "not found" in stderr or "manifest unknown" in stderr:
            return None
        raise
    if not output or output == "null":
        return {}
    labels = json.loads(output)
    return labels if isinstance(labels, dict) else {}


def is_complete(release, labels, force):
    if force or labels is None:
        return False
    return labels.get(MIRROR_LABEL) == UPSTREAM and labels.get(DIGEST_LABEL) == release_digest(release)


def verify_assets(directory):
    checksums = {}
    for line in (directory / "checksums.txt").read_text().splitlines():
        match = re.fullmatch(r"([0-9a-fA-F]{64}) [ *]([^/\\]+)", line)
        if not match or match[2] in checksums or match[2] in (".", ".."):
            raise ValueError("Invalid or duplicate checksum entry")
        checksums[match[2]] = match[1].lower()
    archives = list(directory.glob("denova-*.tar.gz"))
    if not archives:
        raise ValueError("No Linux release archives found")
    for archive in archives:
        if archive.name not in checksums:
            raise ValueError(f"Missing checksum: {archive.name}")
        with archive.open("rb") as stream:
            actual = hashlib.file_digest(stream, "sha256").hexdigest()
        if actual != checksums[archive.name]:
            raise ValueError(f"Checksum mismatch: {archive.name}")


def extract_bundle(archive, destination):
    with tarfile.open(archive, "r:gz") as bundle:
        for member in bundle.getmembers():
            path = PurePosixPath(member.name)
            if (path.is_absolute() or ".." in path.parts or "\\" in member.name
                    or not path.parts or path.parts[0] != "denova"
                    or not (member.isfile() or member.isdir())):
                raise ValueError(f"Unsafe archive member: {member.name}")
        bundle.extractall(destination, filter="data")


def prepare(args):
    release = (find_release(UPSTREAM, args.tag) if args.tag else api(f"repos/{UPSTREAM}/releases/latest"))
    if release is None:
        raise ValueError("Upstream release does not exist")
    validate_release(release)
    tag = release["tag_name"]
    image = f"ghcr.io/{args.repository.lower()}"
    digest = release_digest(release)
    outputs = {"changed": "false", "version": tag, "image": image, "digest": digest}

    existing_labels = inspect_image_labels(f"{image}:{tag}")
    # An existing image without our upstream mirror label belongs to this fork's
    # own source build; never replace it with an upstream build.
    if existing_labels is not None and existing_labels.get(MIRROR_LABEL) != UPSTREAM:
        print(f"{args.repository} publishes its own {image}:{tag}; skipping the upstream mirror")
    elif not is_complete(release, existing_labels, args.force):
        assets = args.directory / "assets"
        assets.mkdir(parents=True, exist_ok=False)
        run(
            "gh", "release", "download", tag,
            "--repo", UPSTREAM,
            "--dir", str(assets),
            "--pattern", "checksums.txt",
            "--pattern", "denova*-linux-*.tar.gz",
        )
        verify_assets(assets)
        for architecture, package in (("amd64", "x64"), ("arm64", "arm64")):
            archive = assets / f"denova-webui-{tag}-linux-{package}.tar.gz"
            if not archive.is_file():
                archive = assets / f"denova-{tag}-linux-{package}.tar.gz"
            if not archive.is_file():
                raise ValueError(f"Missing Linux bundle archive for {architecture}")
            extract_bundle(archive, args.directory / architecture)
            root = args.directory / architecture / "denova"
            # The frontend is embedded in the binary; web/ may be an empty directory.
            for required in ("denova", "denova-updater", "LICENSE"):
                if not (root / required).is_file():
                    raise ValueError(f"Missing bundle file: {architecture}/{required}")
            if not (root / "skills").is_dir():
                raise ValueError(f"Missing bundled Skills: {architecture}")
        outputs["changed"] = "true"

    print(json.dumps(outputs))
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
            output.writelines(f"{key}={value}\n" for key, value in outputs.items())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("prepare",))
    parser.add_argument("--repository", required=True)
    parser.add_argument("--tag", default="")
    parser.add_argument("--force", action="store_true")
    parser.add_argument("--directory", type=Path, default=Path("dist/upstream"))
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repository) or args.repository.lower() == UPSTREAM.lower():
        parser.error("Destination must be a fork, not the upstream repository")
    if args.tag and not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", args.tag):
        parser.error("Expected a stable vMAJOR.MINOR.PATCH tag")
    try:
        prepare(args)
    except subprocess.CalledProcessError as error:
        raise SystemExit(f"Command failed: {error.stderr.strip()}") from error


if __name__ == "__main__":
    main()
