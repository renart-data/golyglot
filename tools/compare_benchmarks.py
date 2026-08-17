#!/usr/bin/env python3
"""Compare median Go benchmark timings from two benchmark output files."""

from __future__ import annotations

import argparse
import re
import statistics
from pathlib import Path


BENCHMARK_LINE = re.compile(
    r"^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([0-9]+(?:\.[0-9]+)?)\s+ns/op(?:\s|$)"
)


def read_samples(path: Path) -> dict[str, list[float]]:
    samples: dict[str, list[float]] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        match = BENCHMARK_LINE.match(line)
        if match is None:
            continue
        samples.setdefault(match.group(1), []).append(float(match.group(2)))
    if not samples:
        raise ValueError(f"{path} contains no Go benchmark samples")
    return samples


def comparison_markdown(
    baseline: dict[str, list[float]], current: dict[str, list[float]]
) -> tuple[str, float]:
    missing = sorted(set(baseline) ^ set(current))
    if missing:
        raise ValueError("benchmark sets differ: " + ", ".join(missing))

    rows = [
        "| Benchmark | Baseline median | Current median | Change | Samples |",
        "| --- | ---: | ---: | ---: | ---: |",
    ]
    largest_regression = float("-inf")
    for name in sorted(baseline):
        baseline_median = statistics.median(baseline[name])
        current_median = statistics.median(current[name])
        change = 100 * (current_median / baseline_median - 1)
        largest_regression = max(largest_regression, change)
        rows.append(
            f"| `{name}` | {baseline_median:.0f} ns/op | "
            f"{current_median:.0f} ns/op | {change:+.1f}% | "
            f"{len(baseline[name])}/{len(current[name])} |"
        )
    return "\n".join(rows) + "\n", largest_regression


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("baseline", type=Path)
    parser.add_argument("current", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument(
        "--fail-regression-percent",
        type=float,
        default=0,
        help="fail if any median regression exceeds this percentage; zero disables",
    )
    args = parser.parse_args()
    if args.fail_regression_percent < 0:
        parser.error("--fail-regression-percent cannot be negative")

    try:
        markdown, largest_regression = comparison_markdown(
            read_samples(args.baseline), read_samples(args.current)
        )
    except ValueError as error:
        parser.error(str(error))
    print(markdown, end="")
    if args.output is not None:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(markdown, encoding="utf-8")

    threshold = args.fail_regression_percent
    if threshold and largest_regression > threshold:
        print(
            f"largest median regression {largest_regression:.1f}% "
            f"exceeds {threshold:.1f}%"
        )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
