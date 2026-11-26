import hashlib
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

import sync_upstream_release as sync


class ReleaseTests(unittest.TestCase):
    def test_select_release_rejects_prerelease_and_unsafe_tags(self):
        for change in ({"prerelease": True}, {"draft": True}, {"tag_name": "v1/../../x"}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                sync.validate_release({"tag_name": "v1.2.3", "draft": False, "prerelease": False, **change})
        sync.validate_release({"tag_name": "v1.2.3", "draft": False, "prerelease": False})

    def test_asset_change_invalidates_digest(self):
        release = {"id": 1, "tag_name": "v1.2.3", "assets": [{"id": 2, "name": "a", "size": 3, "updated_at": "a"}]}
        before = sync.release_digest(release)
        release["assets"][0]["updated_at"] = "b"
        self.assertNotEqual(before, sync.release_digest(release))

    def test_verify_checksums_rejects_corruption_and_missing_coverage(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "denova-v1-linux-x64.tar.gz"
            archive.write_bytes(b"release")
            digest = hashlib.sha256(b"release").hexdigest()
            (root / "checksums.txt").write_text(f"{digest}  {archive.name}\n")
            sync.verify_assets(root)
            archive.write_bytes(b"corrupt")
            with self.assertRaisesRegex(ValueError, "Checksum mismatch"):
                sync.verify_assets(root)
            (root / "checksums.txt").write_text("")
            with self.assertRaisesRegex(ValueError, "Missing checksum"):
                sync.verify_assets(root)

    def test_prepare_extracts_webui_named_linux_archives_only(self):
        release = {"id": 1, "tag_name": "v0.8.0", "draft": False, "prerelease": False, "assets": []}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            download_calls = []

            def fake_run(*args):
                if args[:3] == ("gh", "release", "download"):
                    download_calls.append(args)
                    assets_dir = Path(args[args.index("--dir") + 1])
                    lines = []
                    for package in ("x64", "arm64"):
                        name = f"denova-webui-v0.8.0-linux-{package}.tar.gz"
                        archive = assets_dir / name
                        with tarfile.open(archive, "w:gz") as bundle:
                            for file_name in ("denova/denova", "denova/denova-updater", "denova/LICENSE", "denova/skills/demo.md"):
                                info = tarfile.TarInfo(file_name)
                                info.size = 2
                                bundle.addfile(info, io.BytesIO(b"ok"))
                        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
                        lines.append(f"{digest}  {name}\n")
                    (assets_dir / "checksums.txt").write_text("".join(lines))
                return ""

            output_file = root / "github_output.txt"
            with patch.object(sync, "api", return_value=release), \
                 patch.object(sync, "inspect_image_labels", return_value=None), \
                 patch.object(sync, "run", side_effect=fake_run), \
                 patch.dict("os.environ", {"GITHUB_OUTPUT": str(output_file)}, clear=True):
                sync.prepare(SimpleNamespace(tag="", repository="owner/repo", directory=root / "out", force=False))
                self.assertTrue((root / "out/amd64/denova/denova").is_file())
                self.assertTrue((root / "out/arm64/denova/denova").is_file())
                self.assertEqual(len(download_calls), 1)
                self.assertIn("denova*-linux-*.tar.gz", download_calls[0])
                output_text = output_file.read_text(encoding="utf-8")
                self.assertIn("changed=true\n", output_text)
                self.assertIn(f"digest={sync.release_digest(release)}\n", output_text)

    def test_extract_preserves_bundle_and_rejects_unsafe_members(self):
        for member_name, member_type in [("denova/web/index.html", tarfile.REGTYPE), ("../escape", tarfile.REGTYPE), ("denova/link", tarfile.SYMTYPE)]:
            with self.subTest(member=member_name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                archive = root / "bundle.tar.gz"
                with tarfile.open(archive, "w:gz") as bundle:
                    member = tarfile.TarInfo(member_name)
                    member.type = member_type
                    member.size = 2 if member_type == tarfile.REGTYPE else 0
                    member.linkname = "../../escape" if member_type == tarfile.SYMTYPE else ""
                    bundle.addfile(member, io.BytesIO(b"ok") if member.size else None)
                if member_name == "denova/web/index.html":
                    sync.extract_bundle(archive, root / "out")
                    self.assertEqual((root / "out/denova/web/index.html").read_bytes(), b"ok")
                else:
                    with self.assertRaises(ValueError):
                        sync.extract_bundle(archive, root / "out")

    def test_only_not_found_is_treated_as_missing_image(self):
        for stderr, expected in [
            ("ERROR: ghcr.io/owner/repo:v1.2.3: not found", None),
            ("ERROR: manifest unknown", None),
            ("ERROR: unauthorized: authentication required", "error"),
        ]:
            with self.subTest(stderr=stderr), patch.object(sync, "run", side_effect=subprocess.CalledProcessError(1, "docker", stderr=stderr)):
                if expected is None:
                    self.assertIsNone(sync.inspect_image_labels("ghcr.io/owner/repo:v1.2.3"))
                else:
                    with self.assertRaises(subprocess.CalledProcessError):
                        sync.inspect_image_labels("ghcr.io/owner/repo:v1.2.3")

    def test_inspect_image_labels_parses_json_and_null(self):
        with patch.object(sync, "run", return_value=json.dumps({sync.MIRROR_LABEL: sync.UPSTREAM})):
            self.assertEqual(sync.inspect_image_labels("ghcr.io/owner/repo:v1.2.3"), {sync.MIRROR_LABEL: sync.UPSTREAM})
        with patch.object(sync, "run", return_value="null"):
            self.assertEqual(sync.inspect_image_labels("ghcr.io/owner/repo:v1.2.3"), {})

    def test_completed_mirror_skips_unless_forced_or_digest_changed(self):
        release = {"id": 1, "tag_name": "v1.2.3", "assets": []}
        digest = sync.release_digest(release)
        labels = {sync.MIRROR_LABEL: sync.UPSTREAM, sync.DIGEST_LABEL: digest}
        self.assertTrue(sync.is_complete(release, labels, False))
        self.assertFalse(sync.is_complete(release, labels, True))
        self.assertFalse(sync.is_complete(release, {**labels, sync.DIGEST_LABEL: "old"}, False))
        self.assertFalse(sync.is_complete(release, None, False))

    def test_completed_prepare_does_not_download_or_create_files(self):
        release = {"id": 1, "tag_name": "v1.2.3", "assets": []}
        labels = {sync.MIRROR_LABEL: sync.UPSTREAM, sync.DIGEST_LABEL: sync.release_digest(release)}
        with tempfile.TemporaryDirectory() as directory, \
             patch.object(sync, "api", return_value=release), \
             patch.object(sync, "inspect_image_labels", return_value=labels), \
             patch.object(sync, "run") as command, \
             patch.dict("os.environ", {}, clear=True):
            target = Path(directory) / "output"
            sync.prepare(SimpleNamespace(tag="", repository="owner/repo", directory=target, force=False))
            command.assert_not_called()
            self.assertFalse(target.exists())

    def test_own_source_image_of_same_version_is_never_overwritten(self):
        release = {"id": 1, "tag_name": "v0.8.0", "assets": []}
        own_labels = {"org.opencontainers.image.revision": "8815a3ff"}
        with tempfile.TemporaryDirectory() as directory, \
             patch.object(sync, "api", return_value=release), \
             patch.object(sync, "inspect_image_labels", return_value=own_labels), \
             patch.object(sync, "run") as command, \
             patch.dict("os.environ", {}, clear=True):
            target = Path(directory) / "output"
            sync.prepare(SimpleNamespace(tag="", repository="owner/repo", directory=target, force=True))
            command.assert_not_called()
            self.assertFalse(target.exists())

    def test_find_release_returns_none_only_on_404(self):
        for stderr, expected in [("gh: Not Found (HTTP 404)", None), ("gh: Bad credentials (HTTP 401)", "error")]:
            with patch.object(sync, "run", side_effect=subprocess.CalledProcessError(1, "gh", stderr=stderr)):
                if expected is None:
                    self.assertIsNone(sync.find_release("alfredxw/denova", "v1.2.3"))
                else:
                    with self.assertRaises(subprocess.CalledProcessError):
                        sync.find_release("alfredxw/denova", "v1.2.3")


if __name__ == "__main__":
    unittest.main()
