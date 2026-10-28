#!/usr/bin/python3
"""Time the unchanged ca-certificates initializer in a disposable installation."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import statistics
import subprocess
import sys
import tempfile
import time


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    evidence = Path(__file__).resolve().parent
    binary = Path(sys.argv[1]).resolve()
    if (evidence / "report.json").exists():
        raise SystemExit("Use a new directory for each run; existing evidence is retained.")
    if os.geteuid() == 0 or os.uname().sysname != "Darwin" or os.uname().machine != "arm64":
        raise SystemExit("Requires non-root Apple Silicon macOS.")
    prefix = Path(subprocess.check_output(["/usr/bin/mktemp", "-d", "/tmp/ga.XXXX"], text=True).strip())
    work = Path(tempfile.mkdtemp(prefix="gobrew-apply-profile-", dir="/tmp"))
    report = {"started_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "binary": str(binary), "binary_sha256": sha(binary),
              "prefix": str(prefix), "work": str(work), "commands": [], "samples": [], "passed": False}
    for name in ("home", "cache", "config", "tmp"):
        (work / name).mkdir()
    env = {"HOME": str(work / "home"), "XDG_CONFIG_HOME": str(work / "config"),
           "XDG_CACHE_HOME": str(work / "cache"), "TMPDIR": str(work / "tmp") + "/",
           "PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "SHELL": "/bin/zsh", "LANG": "C",
           "USER": os.environ.get("USER", ""), "LOGNAME": os.environ.get("LOGNAME", ""),
           "GOBREW_NO_AUTO_UPDATE": "1", "GOBREW_NO_MODIFY_PATH": "1", "GOBREW_NO_INSTALL_CLEANUP": "1"}
    gb = [str(binary), "--prefix", str(prefix), "--cache-dir", str(work / "cache"), "--json"]

    def run(label, args, child_env=None, cwd=None):
        with (evidence / (label + ".stdout")).open("wb") as out, (evidence / (label + ".stderr")).open("wb") as err:
            start = time.perf_counter()
            result = subprocess.run(args, env=child_env or env, cwd=cwd, stdout=out, stderr=err)
            elapsed = (time.perf_counter() - start) * 1000
        report["commands"].append({"label": label, "argv": args, "cwd": str(cwd) if cwd else None,
                                   "exit_code": result.returncode, "wall_ms": elapsed})
        if result.returncode:
            raise RuntimeError("%s exited %s; see captured output" % (label, result.returncode))
        return elapsed

    def data(label):
        return json.loads((evidence / (label + ".stdout")).read_text())["data"]

    try:
        run("build", gb + ["--version"])
        report["build"] = data("build")
        if report["build"]["version"] != "dev":
            raise RuntimeError("Use a dev source build to prevent CLI self-update")
        run("catalog", gb + ["update"])
        report["catalog_generation"] = data("catalog")["generation"]
        run("available", gb + ["info", "ca-certificates"])
        formula = data("available")[0]
        if formula["version"] != "2026-10-20" or formula["revision"] != 0:
            raise RuntimeError("Catalog no longer has the benchmark certificate fixture")
        run("install", gb + ["install", "ca-certificates"])
        report["install_timings_ms"] = data("install")["timings_ms"]
        run("installed", gb + ["list"])
        records = data("installed")
        if len(records) != 1 or records[0]["formula"]["name"] != "ca-certificates" or records[0]["status"] != "ready":
            raise RuntimeError("Certificate-only installation is not ready")
        record = records[0]
        report["package_version"] = record["formula"]["version"]
        report["bottle_sha256"] = record["bottle"]["sha256"]
        keg = Path(record["keg"])
        initializer = keg / "libexec/post-install"
        source = keg / "share/ca-certificates/cacert.pem"
        target = prefix / "etc/ca-certificates/cert.pem"
        report["initializer_path"] = str(initializer)
        report["initializer_sha256"] = sha(initializer)
        report["source_sha256"] = sha(source)
        expected = sha(target)
        report["installed_output_sha256"] = expected
        # Match the command runner's environment and working directory. Deleting
        # only the derived output forces each sample to produce a fresh bundle.
        command_env = {key: env[key] for key in ("HOME", "USER", "LOGNAME", "LANG")}
        command_env["PATH"] = str(prefix / "bin") + ":" + str(prefix / "sbin") + ":/usr/bin:/bin:/usr/sbin:/sbin"
        command_env["TMPDIR"] = str(prefix / "var/gobrew/tmp")
        report["command_environment"] = command_env
        for number in range(1, 6):
            target.unlink()
            elapsed = run("initializer-%d" % number, [str(initializer), str(source), str(target)], command_env, keg)
            output = target.read_bytes()
            digest = hashlib.sha256(output).hexdigest()
            certificates = output.count(b"-----BEGIN CERTIFICATE-----")
            if digest != expected or not certificates:
                raise RuntimeError("Initializer output differs from the installed certificate bundle")
            report["samples"].append({"round": number, "wall_ms": elapsed, "output_bytes": len(output),
                                      "certificate_count": certificates, "output_sha256": digest})
        report["initializer_median_ms"] = statistics.median(item["wall_ms"] for item in report["samples"])
        run("doctor", gb + ["doctor"])
        run("uninstall", gb + ["uninstall", "ca-certificates"])
        run("cleanup", gb + ["cleanup"])
        if sha(binary) != report["binary_sha256"]:
            raise RuntimeError("CLI binary changed during profiling")
        report["passed"] = True
    except BaseException as error:
        report["error"] = str(error)
        raise
    finally:
        shutil.rmtree(prefix)
        shutil.rmtree(work)
        report["temporary_directories_removed"] = not prefix.exists() and not work.exists()
        (evidence / "report.json").write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
