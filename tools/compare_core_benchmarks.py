#!/usr/bin/env python3
"""Compare same-runner Golyglot and optimized Polyglot core samples."""

from __future__ import annotations

import argparse
import hashlib
import math
import random
import statistics
from pathlib import Path


def read_samples(path: Path, implementation: str) -> dict[str, dict[int, float]]:
    samples: dict[str, dict[int, float]] = {}
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        fields = line.split("\t")
        if len(fields) != 6:
            raise ValueError(f"{path}:{line_number}: expected six tab-separated fields")
        actual_implementation, benchmark, sample, iterations, elapsed_ns, ns_per_op = fields
        if actual_implementation != implementation:
            raise ValueError(
                f"{path}:{line_number}: expected {implementation!r}, "
                f"got {actual_implementation!r}"
            )
        sample_number = int(sample)
        if not benchmark or sample_number < 1 or int(iterations) < 1 or int(elapsed_ns) < 1:
            raise ValueError(f"{path}:{line_number}: invalid benchmark sample")
        value = float(ns_per_op)
        if not math.isfinite(value) or value <= 0:
            raise ValueError(f"{path}:{line_number}: invalid ns/op value")
        benchmark_samples = samples.setdefault(benchmark, {})
        if sample_number in benchmark_samples:
            raise ValueError(
                f"{path}:{line_number}: duplicate {benchmark} sample {sample_number}"
            )
        benchmark_samples[sample_number] = value
    if not samples:
        raise ValueError(f"{path} contains no benchmark samples")
    return samples


def format_duration(nanoseconds: float) -> str:
    if nanoseconds < 1_000:
        return f"{nanoseconds:.1f} ns/op"
    if nanoseconds < 1_000_000:
        return f"{nanoseconds / 1_000:.3f} us/op"
    return f"{nanoseconds / 1_000_000:.3f} ms/op"


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def paired_bootstrap_median_interval(
    ratios: list[float], seed_text: str, iterations: int = 20_000
) -> tuple[float, float]:
    random_source = random.Random(hashlib.sha256(seed_text.encode()).digest())
    estimates = sorted(
        statistics.median(random_source.choices(ratios, k=len(ratios)))
        for _ in range(iterations)
    )
    return (
        estimates[round((iterations - 1) * 0.025)],
        estimates[round((iterations - 1) * 0.975)],
    )


def comparison_markdown(
    golyglot: dict[str, dict[int, float]],
    polyglot: dict[str, dict[int, float]],
    minimum_samples: int,
    golyglot_binary: Path,
    polyglot_binary: Path,
) -> str:
    missing = sorted(set(golyglot) ^ set(polyglot))
    if missing:
        raise ValueError("benchmark sets differ: " + ", ".join(missing))

    rows = [
        "## Optimized core benchmark",
        "",
        "| Benchmark | Golyglot median | Polyglot median | Faster (95% paired bootstrap CI) | Samples |",
        "| --- | ---: | ---: | ---: | ---: |",
    ]
    ratios: list[float] = []
    for benchmark in sorted(golyglot):
        golyglot_samples = golyglot[benchmark]
        polyglot_samples = polyglot[benchmark]
        if set(golyglot_samples) != set(polyglot_samples):
            raise ValueError(f"{benchmark} sample numbers differ between implementations")
        if len(golyglot_samples) < minimum_samples or len(polyglot_samples) < minimum_samples:
            raise ValueError(
                f"{benchmark} has {len(golyglot_samples)}/{len(polyglot_samples)} "
                f"samples, need at least {minimum_samples}"
            )
        sample_numbers = sorted(golyglot_samples)
        golyglot_values = [golyglot_samples[sample] for sample in sample_numbers]
        polyglot_values = [polyglot_samples[sample] for sample in sample_numbers]
        golyglot_median = statistics.median(golyglot_values)
        polyglot_median = statistics.median(polyglot_values)
        paired_ratios = [
            polyglot_samples[sample] / golyglot_samples[sample]
            for sample in sample_numbers
        ]
        interval_low, interval_high = paired_bootstrap_median_interval(
            paired_ratios, benchmark
        )
        paired_median = statistics.median(paired_ratios)
        ratios.append(paired_median)
        if paired_median >= 1:
            faster = (
                f"Golyglot {paired_median:.2f}x "
                f"({interval_low:.2f}-{interval_high:.2f}x)"
            )
        else:
            faster = (
                f"Polyglot {1 / paired_median:.2f}x "
                f"({1 / interval_high:.2f}-{1 / interval_low:.2f}x)"
            )
        rows.append(
            f"| `{benchmark}` | {format_duration(golyglot_median)} | "
            f"{format_duration(polyglot_median)} | {faster} | "
            f"{len(golyglot_samples)}/{len(polyglot_samples)} |"
        )

    geometric_mean = math.exp(statistics.fmean(math.log(ratio) for ratio in ratios))
    if geometric_mean >= 1:
        aggregate = f"Golyglot {geometric_mean:.2f}x faster"
    else:
        aggregate = f"Polyglot {1 / geometric_mean:.2f}x faster"
    rows.extend(
        [
            "",
            "The faster column uses the median paired ratio; the time columns "
            "show each implementation's independent median.",
            "",
            f"Geometric mean across the matched cases: **{aggregate}**.",
            "",
            "## Stripped runner size",
            "",
            "This is the linked size of equivalent benchmark runners, not a "
            "standalone library-size claim.",
            "",
            "| Runner | Bytes | MiB | SHA-256 |",
            "| --- | ---: | ---: | --- |",
        ]
    )
    for name, path in (
        ("Golyglot", golyglot_binary),
        ("Polyglot", polyglot_binary),
    ):
        size = path.stat().st_size
        rows.append(
            f"| {name} | {size:,} | {size / (1024 * 1024):.2f} | "
            f"`{file_sha256(path)}` |"
        )
    size_ratio = polyglot_binary.stat().st_size / golyglot_binary.stat().st_size
    if size_ratio >= 1:
        size_summary = f"Golyglot is {size_ratio:.2f}x smaller"
    else:
        size_summary = f"Polyglot is {1 / size_ratio:.2f}x smaller"
    rows.extend(["", f"Linked runner footprint: **{size_summary}**.", ""])
    return "\n".join(rows)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("golyglot_samples", type=Path)
    parser.add_argument("polyglot_samples", type=Path)
    parser.add_argument("--golyglot-binary", type=Path, required=True)
    parser.add_argument("--polyglot-binary", type=Path, required=True)
    parser.add_argument("--minimum-samples", type=int, default=5)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.minimum_samples < 1:
        parser.error("--minimum-samples must be positive")

    try:
        markdown = comparison_markdown(
            read_samples(args.golyglot_samples, "golyglot"),
            read_samples(args.polyglot_samples, "polyglot"),
            args.minimum_samples,
            args.golyglot_binary,
            args.polyglot_binary,
        )
    except (OSError, ValueError) as error:
        parser.error(str(error))
    print(markdown, end="")
    if args.output is not None:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(markdown, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
