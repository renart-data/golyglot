#!/usr/bin/env python3
"""Select a deterministic corpus sample shared by Golyglot and Polyglot."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from polyglot_ffi_oracle import call_transpile, has_formatting_newline, load_library


@dataclass(frozen=True)
class Candidate:
    fixture: str
    index: int
    source: str
    target: str
    sql: str
    expected: str

    def identity(self) -> str:
        return f"{self.fixture}\0{self.index}\0{self.source}\0{self.target}"


def read_json(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as stream:
        return json.load(stream)


def candidates(fixture_root: Path) -> list[Candidate]:
    result: list[Candidate] = []
    dialect_root = fixture_root / "dialects"
    for fixture_path in sorted(dialect_root.glob("*.json")):
        fixture = read_json(fixture_path)
        source = str(fixture.get("dialect") or fixture_path.stem)
        relative_path = fixture_path.relative_to(fixture_root).as_posix()
        for index, test_case in enumerate(fixture.get("transpilation", [])):
            sql = str(test_case["sql"])
            for target, expected in sorted(test_case.get("write", {}).items()):
                if target == source:
                    continue
                result.append(
                    Candidate(
                        fixture=relative_path,
                        index=index,
                        source=source,
                        target=str(target),
                        sql=sql,
                        expected=str(expected),
                    )
                )
    return result


def benchmark_name(candidate: Candidate) -> str:
    raw = f"{candidate.source}_to_{candidate.target}_{candidate.index:04d}"
    return re.sub(r"[^a-zA-Z0-9]+", "_", raw).strip("_").lower()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--library", type=Path, required=True)
    parser.add_argument(
        "--fixtures",
        type=Path,
        default=Path("testdata/polyglot/sqlglot_fixtures"),
    )
    parser.add_argument(
        "--output",
        type=Path,
        default=Path("benchmarks/corpus_cases.json"),
    )
    parser.add_argument("--count", type=int, default=64)
    parser.add_argument("--seed", default="golyglot-corpus-v1")
    args = parser.parse_args()

    if args.count < 1:
        parser.error("--count must be positive")
    library_path = args.library.resolve()
    if not library_path.is_file():
        parser.error(f"FFI library does not exist: {library_path}")

    fixture_root = args.fixtures.resolve()
    library = load_library(library_path)
    exact: list[Candidate] = []
    all_candidates = candidates(fixture_root)
    for candidate in all_candidates:
        got, error = call_transpile(
            library,
            candidate.sql,
            candidate.source,
            candidate.target,
            has_formatting_newline(candidate.expected),
        )
        if error is None and got == candidate.expected:
            exact.append(candidate)

    if len(exact) < args.count:
        raise RuntimeError(
            f"only {len(exact)} exact matches are available, need {args.count}"
        )

    def sample_key(candidate: Candidate) -> bytes:
        value = f"{args.seed}\0{candidate.identity()}".encode()
        return hashlib.sha256(value).digest()

    selected = sorted(exact, key=sample_key)[: args.count]
    selected.sort(key=lambda value: (value.source, value.target, value.index))
    manifest = {
        "description": (
            "Deterministic mapping-weighted sample of cross-dialect SQLGlot "
            "fixtures whose expected output is matched exactly by Golyglot "
            "and the released Polyglot FFI."
        ),
        "selection": {
            "algorithm": "lowest SHA-256 over every eligible mapping",
            "seed": args.seed,
            "candidate_mappings": len(all_candidates),
            "exact_match_mappings": len(exact),
            "sample_size": len(selected),
        },
        "cases": [
            {
                "name": benchmark_name(candidate),
                "feature": "deterministic cross-dialect corpus sample",
                "fixture": candidate.fixture,
                "index": candidate.index,
                "target": candidate.target,
            }
            for candidate in selected
        ],
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(
        f"selected {len(selected)} of {len(exact)} exact matches "
        f"from {len(all_candidates)} candidate mappings"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
