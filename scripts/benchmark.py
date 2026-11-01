#!/usr/bin/env python3
"""Measure the spec's targets with 1,000 controlled CLI fixtures.

Usage: python3 scripts/benchmark.py /path/to/shelp [--check]
No installed executables are scanned. Temporary files are removed on exit.
"""
import argparse
import os
from pathlib import Path
import statistics
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("binary", type=Path)
parser.add_argument("--check", action="store_true", help="fail if either target is missed")
args = parser.parse_args()
binary = args.binary.resolve()

with tempfile.TemporaryDirectory(prefix="shelp-benchmark-") as temporary:
    root = Path(temporary)
    commands = root / "bin"
    commands.mkdir()
    topics = ["network socket connection", "files directory storage", "git commit history",
              "archive compress extract", "text search pattern", "process memory system"]
    for i in range(1000):
        name = f"tool{i:04d}"
        topic = topics[i % len(topics)]
        help_text = f"Usage: {name} [options]\n" + "\n".join(
            f"--option{j} {topic} description feature{i % 50} parameter{j}"
            for j in range(120)
        )
        command = commands / name
        command.write_text(f"#!/bin/sh\nprintf '%s\\n' '{help_text}'\n")
        command.chmod(0o755)
    database = root / "index.db"
    environment = dict(os.environ, SHELP_DB=str(database))
    start = time.perf_counter()
    subprocess.run([str(binary), "index", "--path", str(commands)],
                   check=True, env=environment, stdout=subprocess.DEVNULL)
    index_seconds = time.perf_counter() - start
    samples = []
    for i in range(50):
        query = ["tool00", "tol001", "network", "storage", "history"][i % 5]
        start = time.perf_counter()
        result = subprocess.run([str(binary), "complete", "--", query],
                                check=True, env=environment, capture_output=True)
        samples.append(time.perf_counter() - start)
        if not result.stdout:
            raise RuntimeError(f"No completion candidates for {query!r}")
    p95 = sorted(samples)[int(len(samples) * 0.95) - 1]
    print(f"Index 1,000 commands with ~8 KiB help: {index_seconds:.3f} s")
    print(f"Completion, 50 process invocations: median {statistics.median(samples)*1000:.2f} ms, "
          f"p95 {p95*1000:.2f} ms, max {max(samples)*1000:.2f} ms")
    if args.check and (index_seconds >= 30 or max(samples) >= 0.05):
        raise SystemExit("Spec performance target missed on this machine")
