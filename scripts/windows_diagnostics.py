#!/usr/bin/env python3
"""Run interleaved Windows runtime A/B diagnostics without hiding failures."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import time

from patch_go_windows import patch


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rounds", type=int, default=24)
    parser.add_argument("--count", type=int, default=50)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    if not 1 <= args.rounds <= 200 or not 1 <= args.count <= 1000:
        parser.error("rounds must be 1..200 and count 1..1000")
    if sys.platform != "win32":
        parser.error("native Windows execution is required")
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    root = Path(__file__).resolve().parents[1]
    env = os.environ.copy()
    env.update(GOTOOLCHAIN="local", GOTRACEBACK="system")
    env.pop("GODEBUG", None)
    env.pop("GOROOT", None)
    metadata = {"platform": platform.platform(), "python": sys.version, "rounds": args.rounds, "count": args.count}
    def command(name, cmd, run_env=env, timeout=240):
        started = time.monotonic()
        try:
            result = subprocess.run(cmd, cwd=root, env=run_env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
            output, code = result.stdout, result.returncode
        except subprocess.TimeoutExpired as exc:
            output, code = (exc.stdout or b"") + b"\nDIAGNOSTIC PROCESS TIMEOUT\n", 124
        (out / (name + ".log")).write_bytes(output)
        record = {"name": name, "command": [str(x) for x in cmd], "exit_code": code, "seconds": round(time.monotonic() - started, 3), "log": name + ".log"}
        with (out / "results.jsonl").open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(record) + "\n")
        print(json.dumps(record), flush=True)
        return code, output
    command("cpu-os", ["powershell", "-NoProfile", "-Command", "Get-CimInstance Win32_Processor | Select-Object Name,Manufacturer,NumberOfCores,NumberOfLogicalProcessors | ConvertTo-Json; Get-CimInstance Win32_OperatingSystem | Select-Object Caption,Version,BuildNumber | ConvertTo-Json"])
    code, goenv = command("go-env", ["go", "env", "-json"])
    if code:
        raise RuntimeError("cannot inspect Go toolchain")
    metadata["go_env"] = json.loads(goenv)
    metadata["source_commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    metadata["working_tree"] = subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True)
    (out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
    source = Path(metadata["go_env"]["GOROOT"])
    patched = patch(source, out.parent / "go-patched", out / "toolchain-patch")
    binaries = {}
    for arm, toolchain in [("stock", source), ("patched", patched)]:
        arm_env = dict(env, GOROOT=str(toolchain), GOCACHE=str(out.parent / ("gocache-" + arm)))
        executable = out / (arm + ".test.exe")
        code, _ = command("build-" + arm, [str(toolchain / "bin/go.exe"), "test", "-c", "-o", str(executable), "./internal/cli"], arm_env, timeout=360)
        if code:
            raise RuntimeError("diagnostic build failed: " + arm)
        binaries[arm] = executable
    results = []
    signature = b"fatal error: concurrent map writes"
    for iteration in range(args.rounds):
        # Rotate arm order to reduce warm-up and host-load bias.
        arms = ["stock", "asyncpreemptoff", "patched"]
        arms = arms[iteration % 3:] + arms[:iteration % 3]
        for arm in arms:
            binary = binaries["stock" if arm == "asyncpreemptoff" else arm]
            # A distinct file creates a new image section; every arm gets the same treatment.
            cold = out.parent / ("cold-" + arm + "-" + str(iteration) + ".exe")
            shutil.copyfile(binary, cold)
            run_env = dict(env)
            if arm == "asyncpreemptoff":
                run_env["GODEBUG"] = "asyncpreemptoff=1"
            name = f"{iteration + 1:03d}-{arm}"
            code, output = command(name, [str(cold), "-test.run", "^TestHostRetry", "-test.count", str(args.count), "-test.timeout", "45s", "-test.v"], run_env, timeout=55)
            cold.unlink(missing_ok=True)
            results.append({"arm": arm, "round": iteration + 1, "exit_code": code, "original_signature": signature in output})
    summary = {arm: {"runs": sum(x["arm"] == arm for x in results), "failures": sum(x["arm"] == arm and x["exit_code"] != 0 for x in results), "original_signature": sum(x["arm"] == arm and x["original_signature"] for x in results)} for arm in ["stock", "asyncpreemptoff", "patched"]}
    summary["interpretation"] = "Diagnostic evidence only. All-green does not confirm a root cause; compare baseline failures and exact signatures before attributing the original crash."
    summary["binaries_sha256"] = {arm: hashlib.sha256(binary.read_bytes()).hexdigest() for arm, binary in binaries.items()}
    (out / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as stream:
            stream.write("```json\n" + json.dumps(summary, indent=2) + "\n```\n")
    return 1 if any(result["exit_code"] for result in results) else 0


if __name__ == "__main__":
    sys.exit(main())
