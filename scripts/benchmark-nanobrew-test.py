"""Local verification of the comparison's isolation and sample definitions."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("comparison", Path(__file__).with_name("benchmark-nanobrew.py"))
comparison = importlib.util.module_from_spec(spec)
spec.loader.exec_module(comparison)


class ComparisonTests(unittest.TestCase):
    def installed_fixture(self, directory, symlink=False, macho=True):
        root = Path(directory)
        prefix = root / "prefix"
        packages = {"jq": "bin/jq", "oniguruma": "lib/libonig.dylib"}
        for name, filename in packages.items():
            keg = prefix / "Cellar" / name / "1"
            tree = root / "store-relocated" / name if symlink else keg
            file = tree / filename
            file.parent.mkdir(parents=True)
            file.write_bytes((b"\xcf\xfa\xed\xfe" if macho else b"text") + b"fixture")
            if symlink:
                keg.parent.mkdir(parents=True)
                keg.symlink_to(tree, target_is_directory=True)
        (root / "db").mkdir()
        (root / "db/state.json").write_text(json.dumps({"kegs": [{"name": name, "version": "1", "sha256": name} for name in packages]}))
        runner = object.__new__(comparison.Comparison)
        runner.out = root / "evidence"
        runner.out.mkdir()
        runner.command_number = 0
        runner.envs = {"nanobrew": {}}
        runner.run = Mock(side_effect=lambda label, *args, **kwargs: (0, b"42" if label.endswith("-query") else b""))
        return root, prefix, runner

    def test_verification_scans_resolved_kegs_and_counts_libraries(self):
        for symlink in (True, False):
            with self.subTest(symlink=symlink), tempfile.TemporaryDirectory() as directory:
                root, prefix, runner = self.installed_fixture(directory, symlink=symlink)
                with patch.object(comparison, "NB_ROOT", root), patch.object(comparison, "NB_PREFIX", prefix):
                    result = runner.verify("nanobrew", "jq")
                signatures = [call for call in runner.run.call_args_list if call.args[0] == "nanobrew-signature"]
                self.assertEqual(len(signatures), 2, "both executable and library must be verified")
                identity, verification = result
                self.assertEqual(set(identity), {"jq", "oniguruma"})
                for name in identity:
                    self.assertEqual(verification[name]["macho_files"], 1)
                    self.assertEqual(verification[name]["logical_bytes"], 11)
                # Partition accounting excludes symlink destinations. Per-keg
                # accounting above follows the root and counts each tree once.
                self.assertEqual(comparison.logical_bytes(prefix / "Cellar"), 0 if symlink else 22)

    def test_verification_rejects_zero_macho_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root, prefix, runner = self.installed_fixture(directory, macho=False)
            with patch.object(comparison, "NB_ROOT", root), patch.object(comparison, "NB_PREFIX", prefix):
                with self.assertRaisesRegex(RuntimeError, "no Mach-O files"):
                    runner.verify("nanobrew", "jq")

    def test_developer_machine_is_rejected_before_checking_global_paths(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(comparison.os.path, "lexists", side_effect=AssertionError("read global path")):
            with self.assertRaisesRegex(RuntimeError, "disposable GitHub-hosted"):
                comparison.require_disposable_runner()

    def test_existing_runner_installation_is_refused(self):
        with patch.dict(os.environ, {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted"}), patch.object(comparison.platform, "system", return_value="Darwin"), patch.object(comparison.platform, "machine", return_value="arm64"), patch.object(comparison.os.path, "lexists", return_value=True):
            with self.assertRaisesRegex(RuntimeError, "refusing existing path"):
                comparison.require_disposable_runner()

    def test_warm_reset_preserves_cache_but_removes_all_installed_state(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            files = ["prefix/Cellar/jq/1/bin/jq", "prefix/etc/user.conf", "db/state.json", "cache/blobs/blob", "store/blob/tree", "store-relocated/blob/tree", "cache/api/jq.json"]
            for name in files:
                p = root / name
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text(name)
            runner = object.__new__(comparison.Comparison)
            with patch.object(comparison, "NB_ROOT", root):
                runner.reset("nanobrew", cold=False)
                for name in files[:3]:
                    self.assertFalse((root / name).exists(), name)
                for name in files[3:]:
                    self.assertEqual((root / name).read_text(), name)
                runner.reset("nanobrew", cold=True)
                for name in files[3:6]:
                    self.assertFalse((root / name).exists(), name)
                self.assertEqual((root / files[-1]).read_text(), files[-1])

    def test_same_version_with_different_bottle_or_dependency_is_not_comparable(self):
        runner = object.__new__(comparison.Comparison)
        gb = {"jq": {"version": "1", "sha256": "jq-sha", "tag": "arm64_tahoe"}, "oniguruma": {"version": "1", "sha256": "lib-sha"}}
        for nb in ({"jq": {"version": "1", "sha256": "jq-sha"}}, {"jq": {"version": "1", "sha256": "other"}, "oniguruma": {"version": "1", "sha256": "lib-sha"}}):
            with self.assertRaisesRegex(RuntimeError, "differ"):
                runner.check_equal({"gobrew": gb, "nanobrew": nb})


if __name__ == "__main__":
    unittest.main()
