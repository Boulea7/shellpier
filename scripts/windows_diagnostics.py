#!/usr/bin/env python3
"""Run interleaved Windows runtime A/B diagnostics without hiding failures."""
import argparse
import array
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import time
import uuid

from patch_go_windows import patch


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rounds", type=int, default=24)
    parser.add_argument("--count", type=int, default=50)
    parser.add_argument("--full-rounds", type=int, default=3)
    parser.add_argument("--blob-mib", type=int, default=96)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    if not 1 <= args.rounds <= 200 or not 1 <= args.count <= 1000:
        parser.error("rounds must be 1..200 and count 1..1000")
    if not 0 <= args.full_rounds <= 20 or not 8 <= args.blob_mib <= 256:
        parser.error("full-rounds must be 0..20 and blob-mib 8..256")
    if sys.platform != "win32":
        parser.error("native Windows execution is required")
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    root = Path(__file__).resolve().parents[1]
    env = os.environ.copy()
    env.update(GOTOOLCHAIN="local", GOTRACEBACK="system")
    env.pop("GODEBUG", None)
    env.pop("GOROOT", None)
    metadata = {"platform": platform.platform(), "python": sys.version, "rounds": args.rounds, "count": args.count, "full_rounds": args.full_rounds, "blob_mib": args.blob_mib}
    def command(name, cmd, run_env=env, timeout=240, cwd=None):
        started = time.monotonic()
        try:
            result = subprocess.run(cmd, cwd=cwd or root, env=run_env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
            output, code = result.stdout, result.returncode
        except subprocess.TimeoutExpired as exc:
            output, code = (exc.stdout or b"") + b"\nDIAGNOSTIC PROCESS TIMEOUT\n", 124
        (out / (name + ".log")).write_bytes(output)
        record = {"name": name, "command": [str(x) for x in cmd], "exit_code": code, "seconds": round(time.monotonic() - started, 3), "log": name + ".log", "cwd": str(cwd or root), "runtime_environment": {key: run_env.get(key, "") for key in ["GOTOOLCHAIN", "GOTRACEBACK", "GODEBUG", "GOMAXPROCS", "GOEXPERIMENT", "GOROOT", "GOCACHE"]}}
        with (out / "results.jsonl").open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(record) + "\n")
        print(json.dumps(record), flush=True)
        return code, output
    command("cpu-os", ["powershell", "-NoProfile", "-Command", "Get-CimInstance Win32_Processor | Select-Object Name,Manufacturer,Family,Stepping,Revision,ProcessorId,NumberOfCores,NumberOfLogicalProcessors | ConvertTo-Json; Get-CimInstance Win32_OperatingSystem | Select-Object Caption,Version,BuildNumber | ConvertTo-Json"])
    code, goenv = command("go-env", ["go", "env", "-json"])
    if code:
        raise RuntimeError("cannot inspect Go toolchain")
    metadata["go_env"] = json.loads(goenv)
    metadata["source_commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    metadata["working_tree"] = subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True)
    (out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
    source = Path(metadata["go_env"]["GOROOT"])
    patched = patch(source, out.parent / "go-patched", out / "toolchain-patch")
    canary_source = out.parent / "runtime-canary-source"
    canary_source.mkdir(exist_ok=True)
    shutil.copyfile(root / "scripts/windows_runtime_canary/main.go", canary_source / "main.go")
    (canary_source / "go.mod").write_text("module shellpier-runtime-canary\n\ngo 1.27.2\n")
    # Generate real non-sparse bytes. The independent position-dependent pattern
    # deliberately differs from the third-party reproducer used in upstream reports.
    with (canary_source / "blob.bin").open("wb") as blob:
        words = args.blob_mib * 1024 * 1024 // 8
        for start in range(0, words, 32768):
            block = array.array("Q", (((index ^ 0xd6e8feb86659fd93) * 0xa0761d6478bd642f) & ((1 << 64) - 1) for index in range(start, min(start + 32768, words))))
            if sys.byteorder != "little":
                block.byteswap()
            blob.write(block.tobytes())
    metadata["canary_source_sha256"] = hashlib.sha256((canary_source / "main.go").read_bytes()).hexdigest()
    metadata["canary_blob_sha256"] = hashlib.sha256((canary_source / "blob.bin").read_bytes()).hexdigest()
    (out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
    shutil.copyfile(canary_source / "main.go", out / "canary-source.go.txt")
    binaries = {}
    for arm, toolchain in [("stock", source), ("patched", patched)]:
        arm_env = dict(env, GOROOT=str(toolchain), GOCACHE=str(out.parent / ("gocache-" + arm)))
        executable = out / (arm + ".test.exe")
        code, _ = command("build-" + arm, [str(toolchain / "bin/go.exe"), "test", "-c", "-o", str(executable), "./internal/cli"], arm_env, timeout=360)
        if code:
            raise RuntimeError("diagnostic build failed: " + arm)
        binaries[("host-retry", arm)] = executable
        binaries[("cli-full", arm)] = executable
        executable = out.parent / (arm + "-canary.exe")
        # Build the standalone module with only stdlib and the generated image.
        go = str(toolchain / "bin/go.exe")
        code, _ = command("build-canary-" + arm, [go, "-C", str(canary_source), "build", "-tags", "shellpier_runtime_canary", "-o", str(executable), "."], arm_env, timeout=180)
        if code:
            raise RuntimeError("canary build failed: " + arm)
        binaries[("canary", arm)] = executable
    results = []
    created_cold_files = set()
    cleanup_warnings = {}
    execution_id = uuid.uuid4().hex[:12]

    def cleanup_cold(path):
        if path not in created_cold_files:
            raise ValueError("refusing to remove a file not created by this diagnostic run")
        for delay in [0, 0.05, 0.1, 0.2, 0.4]:
            if delay:
                time.sleep(delay)
            try:
                path.unlink(missing_ok=True)
                created_cold_files.remove(path)
                return
            except PermissionError as error:
                # Antivirus or the loader can retain a terminated image briefly.
                # Keep the workload outcome; retry this owned file at the end.
                last_error = error
        cleanup_warnings[str(path)] = {"path": str(path), "error": str(last_error), "winerror": getattr(last_error, "winerror", None)}
        (out / "cleanup-warnings.json").write_text(json.dumps(list(cleanup_warnings.values()), indent=2) + "\n")
        print("CLEANUP_WARNING " + str(last_error), flush=True)

    signature = b"fatal error: concurrent map writes"
    for workload in ["canary", "host-retry", "cli-full"]:
        for iteration in range(args.full_rounds if workload == "cli-full" else args.rounds):
            # Rotate arm order to reduce warm-up and host-load bias.
            arms = ["stock", "asyncpreemptoff", "patched"]
            arms = arms[iteration % 3:] + arms[:iteration % 3]
            for arm in arms:
                binary = binaries[(workload, "stock" if arm == "asyncpreemptoff" else arm)]
                # A distinct file creates a new image section; physical hard faults
                # remain OS-dependent and are not claimed merely from a fresh name.
                cold = out.parent / ("cold-" + execution_id + "-" + workload + "-" + arm + "-" + str(iteration) + ".exe")
                if cold.exists():
                    raise ValueError("refusing to overwrite an existing cold image")
                shutil.copyfile(binary, cold)
                created_cold_files.add(cold)
                runtime_arm = "patched" if arm == "patched" else "stock"
                toolchain = patched if runtime_arm == "patched" else source
                run_env = dict(env, GOROOT=str(toolchain), GOCACHE=str(out.parent / ("gocache-" + runtime_arm)))
                run_env["PATH"] = str(toolchain / "bin") + os.pathsep + env.get("PATH", "")
                if arm == "asyncpreemptoff":
                    run_env["GODEBUG"] = "asyncpreemptoff=1"
                name = f"{workload}-{iteration + 1:03d}-{arm}"
                options = []
                if workload == "host-retry":
                    options = ["-test.run", "^TestHostRetry", "-test.count", str(args.count), "-test.timeout", "45s", "-test.v"]
                elif workload == "cli-full":
                    options = ["-test.count", "1", "-test.timeout", "120s", "-test.v"]
                code, output = command(name, [str(cold)] + options, run_env, timeout=130 if workload == "cli-full" else 55, cwd=root / "internal/cli" if workload != "canary" else root)
                if workload == "canary" and code == 0 and b"CANARY_OK" not in output:
                    code = 125
                results.append({"workload": workload, "arm": arm, "round": iteration + 1, "exit_code": code, "original_signature": signature in output})
                (out / "observations.json").write_text(json.dumps(results, indent=2) + "\n")
                cleanup_cold(cold)
    for cold in list(created_cold_files):
        cleanup_cold(cold)
    summary = {workload: {arm: {"runs": sum(x["workload"] == workload and x["arm"] == arm for x in results), "failures": sum(x["workload"] == workload and x["arm"] == arm and x["exit_code"] != 0 for x in results), "original_signature": sum(x["workload"] == workload and x["arm"] == arm and x["original_signature"] for x in results)} for arm in ["stock", "asyncpreemptoff", "patched"]} for workload in ["canary", "host-retry", "cli-full"]}
    summary["cleanup"] = {"warnings": list(cleanup_warnings.values()), "pending_files": sorted(str(path) for path in created_cold_files), "affects_workload_results": False}
    summary["interpretation"] = "Diagnostic evidence only. All-green does not confirm a root cause; compare baseline failures and exact signatures before attributing the original crash."
    summary["binaries_sha256"] = {workload + "/" + arm: hashlib.sha256(binary.read_bytes()).hexdigest() for (workload, arm), binary in binaries.items()}
    (out / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as stream:
            stream.write("```json\n" + json.dumps(summary, indent=2) + "\n```\n")
    return 1 if any(result["exit_code"] for result in results) else 0


if __name__ == "__main__":
    sys.exit(main())
