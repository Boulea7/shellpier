#!/usr/bin/env python3
"""Copy Go 1.27.2 and backport the Windows guard from upstream CL 828906.

Only an isolated destination is modified. This is diagnostic infrastructure,
not a change to the developer's installed toolchain or release configuration.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil

UPSTREAM = "0042b271db9c51f90d1887435026562a0778d7d9"
CONSTANTS = """
// GetThreadContext kernel state reporting, from winnt.h.
const (
	CONTEXT_EXCEPTION_ACTIVE    = 0x08000000
	CONTEXT_SERVICE_ACTIVE      = 0x10000000
	CONTEXT_EXCEPTION_REQUEST   = 0x40000000
	CONTEXT_EXCEPTION_REPORTING = 0x80000000
)
"""
CONTEXT = "\tc.ContextFlags = windows.CONTEXT_CONTROL\n\n\t// Serialize thread suspension."
NEW_CONTEXT = "\t// CONTEXT_EXCEPTION_REQUEST reports whether the thread is in the kernel.\n\tc.ContextFlags = windows.CONTEXT_CONTROL | windows.CONTEXT_EXCEPTION_REQUEST\n\n\t// Serialize thread suspension."
CONDITION = "\t// Does it want a preemption and is it safe to preempt?\n\tgp := gFromSP(mp, c.SP())\n\tif gp != nil && wantAsyncPreempt(gp) {"
NEW_CONDITION = """	// Don't redirect a thread that is in the kernel handling an exception
	// or system call; Windows may not apply the new context correctly
	// (#79249). Without CONTEXT_EXCEPTION_REPORTING we can't tell.
	safe := c.ContextFlags&windows.CONTEXT_EXCEPTION_REPORTING != 0 &&
		c.ContextFlags&(windows.CONTEXT_EXCEPTION_ACTIVE|windows.CONTEXT_SERVICE_ACTIVE) == 0
	c.ContextFlags = windows.CONTEXT_CONTROL

	// Does it want a preemption and is it safe to preempt?
	gp := gFromSP(mp, c.SP())
	if safe && gp != nil && wantAsyncPreempt(gp) {"""


def patch(source: Path, destination: Path, evidence: Path):
    source, destination = source.resolve(), destination.resolve()
    if source == destination or source in destination.parents or destination in source.parents:
        raise ValueError("source and destination toolchains must be disjoint")
    if (source / "VERSION").read_text().splitlines()[0] != "go1.27.2":
        raise ValueError("only stock go1.27.2 is supported")
    paths = ["src/internal/runtime/syscall/windows/defs_windows.go", "src/runtime/os_windows.go"]
    original = [(source / name).read_bytes() for name in paths]
    defs, runtime = [data.decode() for data in original]
    if "CONTEXT_EXCEPTION_REQUEST" in defs:
        raise ValueError("source toolchain is already patched")
    if runtime.count(CONTEXT) != 1 or runtime.count(CONDITION) != 1:
        raise ValueError("Go runtime does not match the expected 1.27.2 preemption code")
    modified = [(defs + CONSTANTS).encode(), runtime.replace(CONTEXT, NEW_CONTEXT).replace(CONDITION, NEW_CONDITION).encode()]
    marker = destination / "shellpier-windows-patch.json"
    if destination.exists():
        if not marker.is_file() or json.loads(marker.read_text()).get("upstream") != UPSTREAM:
            raise ValueError("refusing to overwrite an existing toolchain destination")
        if any((destination / name).read_bytes() != data for name, data in zip(paths, modified)):
            raise ValueError("existing patched toolchain has unexpected contents")
    else:
        shutil.copytree(source, destination)
        for name, data in zip(paths, modified):
            (destination / name).write_bytes(data)
    evidence.mkdir(parents=True, exist_ok=True)
    records = []
    for name, before, after in zip(paths, original, modified):
        for label, data in [("before", before), ("after", after)]:
            target = evidence / label / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        records.append({"path": name, "before_sha256": hashlib.sha256(before).hexdigest(), "after_sha256": hashlib.sha256(after).hexdigest()})
    manifest = {"upstream": UPSTREAM, "version": "go1.27.2", "adaptation": "Retain release branch wantAsyncPreempt condition and add upstream safe guard", "files": records}
    marker.write_text(json.dumps(manifest, indent=2) + "\n")
    (evidence / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return destination


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--destination", required=True, type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    print(patch(args.source, args.destination, args.evidence))


if __name__ == "__main__":
    main()
