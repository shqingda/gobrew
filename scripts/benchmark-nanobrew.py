#!/usr/bin/env python3
"""Compare unmodified binaries on one disposable GitHub-hosted macOS runner.

Never run on a developer machine. nanobrew has compiled-in /opt paths, so a
temporary HOME alone is insufficient isolation. All measured child processes
are additionally denied access to the runner's preinstalled Homebrew.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.request

NB_VERSION = "0.1.212"
NB_COMMIT = "9be557eb99bcac7f4ea9a489ec7d090c29221f08"
NB_ARCHIVE_SHA = "17d11f5f0b727db70ef6a330903085999cf50f6ac0733efaffc6615c72727b3a"
NB_BINARY_SHA = "be6d44a25aea87560b9d05cd4bcaef16a540d50372db4d32212c19de2fd713ff"
NB_URL = f"https://github.com/justrach/nanobrew/releases/download/v{NB_VERSION}/nb-arm64-apple-darwin.tar.gz"
NB_ROOT = Path("/opt/nanobrew")
NB_PREFIX = NB_ROOT / "prefix"
NB_SHORT = Path("/opt/nb")


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def logical_bytes(path):
    return sum(p.stat().st_size for p in Path(path).rglob("*") if p.is_file() and not p.is_symlink())


def require_disposable_runner():
    if (os.getenv("GITHUB_ACTIONS") != "true" or
            os.getenv("RUNNER_ENVIRONMENT") != "github-hosted" or
            platform.system() != "Darwin" or platform.machine() != "arm64"):
        raise RuntimeError("requires a disposable GitHub-hosted macOS ARM64 runner")
    for path in (NB_ROOT, NB_SHORT):
        if os.path.lexists(path):
            raise RuntimeError(f"refusing existing path: {path}")


class Comparison:
    def __init__(self, args):
        self.args = args
        self.out = Path(args.output).resolve()
        self.out.mkdir(parents=True, exist_ok=False)
        self.report = {
            "schema_version": 1, "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "source_commit": os.getenv("GITHUB_SHA", ""),
            "run_url": f"https://github.com/{os.getenv('GITHUB_REPOSITORY', '')}/actions/runs/{os.getenv('GITHUB_RUN_ID', '')}",
            "runner_image": os.getenv("ImageVersion", ""), "platform": platform.platform(),
            "nanobrew": {"version": NB_VERSION, "commit": NB_COMMIT, "archive_sha256": NB_ARCHIVE_SHA, "sha256": NB_BINARY_SHA, "url": NB_URL},
            "gobrew_sha256": digest(args.gobrew), "samples": [], "commands": [], "cases": {},
            "notes": [
                "Both tools run sequentially on the same disposable GitHub-hosted macOS ARM64 machine, not the developer's M3 Pro.",
                "nanobrew is an unmodified release binary using its compiled-in prefix; gobrew uses a short temporary prefix.",
                "Preinstalled Homebrew and /usr/local are denied to measured processes by macOS sandbox-exec.",
                "Catalog refresh is outside install timing. Cold clears artifact caches; warm clears installed state and mutable configuration only; no-op preserves installed state.",
                "Both empty prefixes are initialized before timing. The one-off uv preflight is recorded separately and excluded from the five-round summary.",
                "gobrew validates signed metadata; nanobrew's native Homebrew API path uses HTTPS JSON and bottle SHA. This trust difference is disclosed, not removed from gobrew.",
                "Comparability requires identical installed package sets, versions including revisions, and bottle SHA. Matching SHA binds nanobrew's bottle bytes to gobrew's selected tag; nanobrew state has no dependency list, so raw dependency metadata fields are not compared.",
                "All executable, signature, linkage and installed-artifact checks run outside timing. Each installed keg must contain at least one verified Mach-O executable or library.",
                "Disk fields are logical regular-file bytes, not APFS allocation. Partition fields do not follow symlinks. installed_keg_bytes resolves keg roots and overlaps the prefix or store partitions; do not add it to those partitions or infer physical savings from clonefile copies.",
            ],
        }
        self.work = None
        self.gb_prefix = None
        self.owns_root = False
        self.owns_link = False
        self.command_number = 0

    def save(self):
        (self.out / "report.json").write_text(json.dumps(self.report, indent=2) + "\n")

    def run(self, label, command, env=None, sandbox=True, stdin=None):
        self.command_number += 1
        name = f"{self.command_number:04d}-{label}"
        argv = [str(x) for x in command]
        if sandbox:
            argv = ["/usr/bin/sandbox-exec", "-f", str(self.profile), *argv]
        start = time.perf_counter_ns()
        try:
            result = subprocess.run(argv, input=stdin, capture_output=True, env=env, timeout=600)
        except subprocess.TimeoutExpired as error:
            result = subprocess.CompletedProcess(argv, 124, error.stdout or b"", error.stderr or b"timeout after 600 seconds")
        elapsed = (time.perf_counter_ns() - start) / 1e6
        (self.out / f"{name}.stdout").write_bytes(result.stdout)
        (self.out / f"{name}.stderr").write_bytes(result.stderr)
        self.report["commands"].append({"label": label, "argv": argv, "exit_code": result.returncode, "milliseconds": elapsed, "stdout": f"{name}.stdout", "stderr": f"{name}.stderr"})
        self.save()
        if result.returncode:
            raise RuntimeError(f"{label} exited {result.returncode}: {result.stderr.decode(errors='replace')[-1500:]}")
        return elapsed, result.stdout

    def setup(self):
        require_disposable_runner()
        self.work = Path(tempfile.mkdtemp(prefix="nb-compare-", dir="/tmp"))
        self.gb_prefix = Path(tempfile.mkdtemp(prefix="", dir="/tmp"))
        self.gb_cache = self.work / "gobrew-cache"
        self.profile = self.work / "isolation.sb"
        self.profile.write_text('(version 1)\n(allow default)\n(deny file-read* file-write* (subpath "/opt/homebrew") (subpath "/usr/local"))\n')
        self.envs = {}
        for manager in ("gobrew", "nanobrew"):
            home = self.work / manager / "home"
            temp = self.work / manager / "tmp"
            home.mkdir(parents=True)
            temp.mkdir(parents=True)
            self.envs[manager] = {
                "HOME": str(home), "TMPDIR": str(temp), "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
                "XDG_CONFIG_HOME": str(home / "config"), "XDG_CACHE_HOME": str(home / "cache"),
                "GOBREW_NO_AUTO_UPDATE": "1", "GOBREW_NO_INSTALL_CLEANUP": "1", "GOBREW_NO_MODIFY_PATH": "1",
                "NANOBREW_DISABLE_UPSTREAM": "1", "NANOBREW_NO_TELEMETRY": "1", "NB_BENCH": "1",
            }
        archive = self.work / "nb.tar.gz"
        with urllib.request.urlopen(NB_URL, timeout=120) as source:
            archive.write_bytes(source.read())
        if digest(archive) != NB_ARCHIVE_SHA:
            raise RuntimeError("nanobrew release archive digest mismatch")
        self.run("extract-nanobrew", ["/usr/bin/tar", "-xzf", archive, "-C", self.work, "nb"], sandbox=False)
        self.nb = self.work / "nb"
        if digest(self.nb) != NB_BINARY_SHA:
            raise RuntimeError("nanobrew executable digest mismatch")
        self.run("create-nanobrew-root", ["/usr/bin/sudo", "-n", "/bin/mkdir", str(NB_ROOT)], sandbox=False)
        self.owns_root = True
        self.run("own-nanobrew-root", ["/usr/bin/sudo", "-n", "/usr/sbin/chown", str(os.getuid()), str(NB_ROOT)], sandbox=False)
        self.run("create-nanobrew-short-link", ["/usr/bin/sudo", "-n", "/bin/ln", "-s", str(NB_PREFIX), str(NB_SHORT)], sandbox=False)
        self.owns_link = True
        self.reset("nanobrew", cold=True)
        self.report["prefixes"] = {"gobrew": str(self.gb_prefix), "nanobrew": str(NB_PREFIX)}
        self.gb = [str(Path(self.args.gobrew).resolve()), "--prefix", str(self.gb_prefix), "--cache-dir", str(self.gb_cache), "--json"]
        self.run("gobrew-update", [*self.gb, "update"], self.envs["gobrew"])
        self.run("nanobrew-uv-metadata", [self.nb, "info", "uv"], self.envs["nanobrew"])
        # Test the denial itself before trusting the profile for a measurement.
        self.run("sandbox-check", ["/bin/sh", "-c", "! /bin/ls /opt/homebrew >/dev/null 2>&1 && ! /bin/ls /usr/local >/dev/null 2>&1"], self.envs["gobrew"])

    def reset(self, manager, cold):
        if manager == "gobrew":
            shutil.rmtree(self.gb_prefix)
            self.gb_prefix.mkdir()
            if cold and self.gb_cache.exists():
                for p in self.gb_cache.iterdir():
                    if p.name != "catalog":
                        shutil.rmtree(p) if p.is_dir() else p.unlink()
            self.run("gobrew-init", [*self.gb, "init"], self.envs["gobrew"])
        else:
            for name in ("prefix", "db"):
                shutil.rmtree(NB_ROOT / name, ignore_errors=True)
            if cold:
                for name in ("store", "store-relocated", "cache/blobs", "cache/tmp", "cache/tokens"):
                    shutil.rmtree(NB_ROOT / name, ignore_errors=True)
            for name in ("prefix/Cellar", "prefix/bin", "prefix/opt", "prefix/lib", "prefix/include", "prefix/share", "store", "store-relocated", "cache/blobs", "cache/tmp", "cache/api", "cache/tokens", "config", "db", "locks"):
                (NB_ROOT / name).mkdir(parents=True, exist_ok=True)

    def verify(self, manager, token):
        prefix = self.gb_prefix if manager == "gobrew" else NB_PREFIX
        env = self.envs[manager]
        if manager == "gobrew":
            _, output = self.run("gobrew-state", [*self.gb, "list"], env)
            records = json.loads(output)["data"]
            identity = {}
            for r in records:
                if r["status"] != "ready" or not r["post_install_complete"]:
                    raise RuntimeError("gobrew installation is not ready")
                f, bottle = r["formula"], r["bottle"]
                version = f["version"] + (f"_{f['revision']}" if f["revision"] else "")
                identity[f["name"]] = {"version": version, "sha256": bottle["sha256"], "tag": bottle["tag"], "dependencies": sorted(f.get("dependencies", []))}
        else:
            state = (NB_ROOT / "db/state.json").read_bytes()
            (self.out / f"{self.command_number:04d}-nanobrew-state.json").write_bytes(state)
            identity = {r["name"]: {"version": r["version"], "sha256": r["sha256"]} for r in json.loads(state)["kegs"]}
        command = [prefix / "bin" / token, "--version"]
        if token == "hello":
            command = [prefix / "bin/hello"]
        _, output = self.run(f"{manager}-{token}-exec", command, env)
        if token == "hello" and b"Hello, world!" not in output:
            raise RuntimeError("hello output mismatch")
        if token == "jq":
            _, output = self.run(f"{manager}-jq-query", [prefix / "bin/jq", "-r", ".answer"], env, stdin=b'{"answer":42}')
            if output.strip() != b"42":
                raise RuntimeError("jq result mismatch")
        if token not in identity:
            raise RuntimeError(f"requested package missing from installed state: {token}")
        return identity, self.verify_kegs(manager, identity)

    def verify_kegs(self, manager, identity):
        prefix = self.gb_prefix if manager == "gobrew" else NB_PREFIX
        owned_root = (prefix if manager == "gobrew" else NB_ROOT).resolve()
        env = self.envs[manager]
        verification = {}
        for name, record in identity.items():
            # Resolve the recorded keg before walking: rglob alone skips a
            # symlinked keg. Fixed nanobrew uses cloneTree/copy today.
            tree = (prefix / "Cellar" / name / record["version"]).resolve(strict=True)
            if not tree.is_relative_to(owned_root):
                raise RuntimeError(f"keg is outside the owned installation: {tree}")
            files = [p for p in tree.rglob("*") if p.is_file() and not p.is_symlink()]
            checked = 0
            for file in files:
                with file.open("rb") as stream:
                    magic = stream.read(4)
                if magic not in (b"\xcf\xfa\xed\xfe", b"\xfe\xed\xfa\xcf", b"\xca\xfe\xba\xbe", b"\xca\xfe\xba\xbf"):
                    continue
                self.run(f"{manager}-signature", ["/usr/bin/codesign", "--verify", file], env)
                _, linked = self.run(f"{manager}-linkage", ["/usr/bin/otool", "-L", file], env)
                for line in linked.decode().splitlines()[1:]:
                    library = line.strip().split(" (", 1)[0]
                    if library.startswith(("/opt/homebrew/", "/usr/local/")):
                        raise RuntimeError(f"foreign dependency: {library}")
                    if library.startswith("/") and not library.startswith((str(prefix) + "/", "/opt/nb/" if manager == "nanobrew" else str(prefix) + "/", "/usr/lib/", "/System/Library/")):
                        raise RuntimeError(f"unexpected dependency: {library}")
                checked += 1
            if not checked:
                raise RuntimeError(f"no Mach-O files verified for {manager} keg {name}: {tree}")
            verification[name] = {"keg_path": str(tree), "macho_files": checked, "logical_bytes": sum(p.stat().st_size for p in files)}
        return verification

    def install(self, manager, token, label, round_number=0):
        command = [*self.gb, "install", token] if manager == "gobrew" else [self.nb, "install", token]
        elapsed, output = self.run(f"{label}-{manager}-{token}", command, self.envs[manager])
        install_stderr = self.out / self.report["commands"][-1]["stderr"]
        identity, verification = self.verify(manager, token)
        sample = {"manager": manager, "token": token, "scenario": label, "round": round_number, "milliseconds": elapsed, "installed": identity,
                  "verification": verification,
                  "disk": {"installed_keg_bytes": sum(v["logical_bytes"] for v in verification.values()),
                           "prefix_bytes": logical_bytes(self.gb_prefix if manager == "gobrew" else NB_PREFIX),
                           "download_bytes": logical_bytes(self.gb_cache / "blobs" if manager == "gobrew" else NB_ROOT / "cache/blobs"),
                           "prepared_bytes": logical_bytes(self.gb_cache / "trees" if manager == "gobrew" else NB_ROOT / "store-relocated"),
                           "extracted_bytes": 0 if manager == "gobrew" else logical_bytes(NB_ROOT / "store"),
                           "metadata_bytes": logical_bytes(self.gb_cache / "catalog" if manager == "gobrew" else NB_ROOT / "cache/api")}}
        if manager == "gobrew":
            data = json.loads(output)["data"]
            sample["timings_ms"] = data.get("timings_ms")
            if label == "no-op" and (data.get("succeeded") or any((data.get("timings_ms") or {}).get(phase, 0) for phase in ("prepare", "apply"))):
                raise RuntimeError("gobrew no-op unexpectedly prepared/applied packages")
        else:
            stderr = install_stderr.read_text(errors="replace")
            sample["phase_timings"] = [{"package": package, "phase": phase, "milliseconds": int(milliseconds)} for package, phase, milliseconds in re.findall(r"\[nb-bench\] (\S+) ([^:\n]+): (\d+)ms", stderr)]
        self.report["samples"].append(sample)
        self.save()
        return identity

    def check_equal(self, pair):
        gb, nb = pair["gobrew"], pair["nanobrew"]
        expected = {k: {"version": v["version"], "sha256": v["sha256"]} for k, v in gb.items()}
        if expected != nb:
            raise RuntimeError(f"installed versions/dependency closure/bottle SHA differ: {expected} != {nb}")

    def preflight(self):
        identities = {}
        for manager in ("gobrew", "nanobrew"):
            self.reset(manager, cold=True)
            identities[manager] = self.install(manager, "uv", "preflight-cold")
        self.check_equal(identities)
        self.report["preflight"] = "passed"
        self.save()

    def compare_case(self, token):
        # Resolve the signed closure before timing, then warm only nanobrew's
        # metadata for those same names. Artifact caches are cleared below.
        self.reset("gobrew", cold=True)
        _, output = self.run(f"gobrew-{token}-plan", [*self.gb, "install", "--dry-run", token], self.envs["gobrew"])
        plan = json.loads(output)["data"]
        expected = {}
        for package in plan["packages"]:
            f, bottle = package["formula"], package["bottle"]
            expected[f["name"]] = {"version": f["version"] + (f"_{f['revision']}" if f["revision"] else ""), "sha256": bottle["sha256"]}
            self.run(f"nanobrew-{f['name']}-metadata", [self.nb, "info", f["name"]], self.envs["nanobrew"])
        self.report["cases"][token] = {"status": "running", "catalog_generation": plan["catalog_generation"], "expected": expected}
        self.save()
        for round_number in range(1, 6):
            order = ("gobrew", "nanobrew") if round_number % 2 else ("nanobrew", "gobrew")
            self.report["cases"][token].setdefault("orders", []).append(list(order))
            for scenario in ("cold", "warm", "no-op"):
                identities = {}
                for manager in order:
                    if scenario != "no-op":
                        self.reset(manager, cold=scenario == "cold")
                    identities[manager] = self.install(manager, token, scenario, round_number)
                self.check_equal(identities)
                actual = {k: {"version": v["version"], "sha256": v["sha256"]} for k, v in identities["gobrew"].items()}
                if actual != expected:
                    raise RuntimeError("installed artifacts differ from the frozen signed plan")
        self.report["cases"][token]["status"] = "passed"
        summary = {}
        for scenario in ("cold", "warm", "no-op"):
            values = {}
            for manager in ("gobrew", "nanobrew"):
                timings = [s["milliseconds"] for s in self.report["samples"] if s["token"] == token and s["scenario"] == scenario and s["manager"] == manager]
                values[manager] = {"milliseconds": timings, "median_ms": statistics.median(timings), "p95_ms": max(timings)}
            values["gobrew_to_nanobrew_time_ratio"] = values["gobrew"]["median_ms"] / values["nanobrew"]["median_ms"]
            summary[scenario] = values
        self.report["cases"][token]["summary"] = summary
        self.save()

    def compare_all(self):
        failed = False
        for token in ("uv", "hello", "jq"):
            try:
                self.compare_case(token)
            except Exception as error:
                failed = True
                case = self.report["cases"].setdefault(token, {})
                case.update({"status": "failed-or-not-comparable", "error": str(error)})
                self.save()
        if failed:
            raise RuntimeError("one or more package comparisons failed; see cases and raw logs")

    def close(self):
        if self.owns_link:
            self.run("remove-nanobrew-short-link", ["/usr/bin/sudo", "-n", "/bin/rm", str(NB_SHORT)], sandbox=False)
        if self.owns_root:
            self.run("remove-nanobrew-root", ["/usr/bin/sudo", "-n", "/bin/rm", "-rf", str(NB_ROOT)], sandbox=False)
        if self.work:
            shutil.rmtree(self.work)
        if self.gb_prefix:
            shutil.rmtree(self.gb_prefix)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gobrew", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--preflight-only", action="store_true", help="stop after the untimed-summary uv preflight")
    args = parser.parse_args()
    comparison = Comparison(args)
    try:
        comparison.setup()
        comparison.preflight()
        if not args.preflight_only:
            comparison.compare_all()
    except Exception as error:
        comparison.report["error"] = str(error)
        print(error, file=sys.stderr)
        return 1
    finally:
        comparison.save()
        comparison.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
