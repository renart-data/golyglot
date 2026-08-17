#!/usr/bin/env python3
"""Validate and benchmark the shared fixture-derived transpilation cases."""

from __future__ import annotations

import argparse
import json
import subprocess
from pathlib import Path
from typing import Any

from polyglot_ffi_oracle import call_transpile, has_formatting_newline, load_library


def read_json(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as stream:
        return json.load(stream)


def load_cases(manifest_path: Path, fixture_root: Path) -> list[dict[str, str]]:
    manifest = read_json(manifest_path)
    references = manifest.get("cases")
    if not isinstance(references, list) or not references:
        raise ValueError(f"{manifest_path} has no benchmark cases")

    fixture_root = fixture_root.resolve()
    fixtures: dict[Path, dict[str, Any]] = {}
    names: set[str] = set()
    cases: list[dict[str, str]] = []
    for reference in references:
        name = str(reference.get("name") or "")
        feature = str(reference.get("feature") or "")
        target = str(reference.get("target") or "")
        if not name or not feature or not target:
            raise ValueError(f"invalid benchmark reference: {reference!r}")
        if name in names:
            raise ValueError(f"duplicate benchmark name: {name}")
        names.add(name)

        fixture_path = (fixture_root / str(reference.get("fixture") or "")).resolve()
        if not fixture_path.is_relative_to(fixture_root):
            raise ValueError(f"fixture escapes fixture root: {fixture_path}")
        fixture = fixtures.get(fixture_path)
        if fixture is None:
            fixture = read_json(fixture_path)
            fixtures[fixture_path] = fixture
        index = int(reference.get("index", -1))
        transpilation = fixture.get("transpilation", [])
        if index < 0 or index >= len(transpilation):
            raise ValueError(f"{fixture_path} has no transpilation index {index}")
        test_case = transpilation[index]
        expected = test_case.get("write", {}).get(target)
        if expected is None:
            raise ValueError(f"{fixture_path}:{index} has no {target!r} target")
        source = str(fixture.get("dialect") or fixture_path.stem)
        cases.append(
            {
                "name": name,
                "feature": feature,
                "sql": str(test_case["sql"]),
                "source": source,
                "target": target,
                "expected": str(expected),
            }
        )
    return cases


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--library", type=Path, required=True)
    parser.add_argument(
        "--manifest",
        type=Path,
        default=Path("benchmarks/fixture_cases.json"),
    )
    parser.add_argument(
        "--fixture-root",
        type=Path,
        default=Path("testdata/polyglot/sqlglot_fixtures"),
    )
    parser.add_argument(
        "--validate-only",
        action="store_true",
        help="validate all selected outputs without running timed samples",
    )
    args = parser.parse_args()

    binary = args.binary.resolve()
    if not args.validate_only and not binary.is_file():
        parser.error(f"benchmark binary does not exist: {binary}")
    library_path = args.library.resolve()
    if not library_path.is_file():
        parser.error(f"FFI library does not exist: {library_path}")

    cases = load_cases(args.manifest, args.fixture_root)
    library = load_library(library_path)
    for case in cases:
        got, error = call_transpile(
            library,
            case["sql"],
            case["source"],
            case["target"],
            has_formatting_newline(case["expected"]),
        )
        if error is not None:
            raise RuntimeError(f"{case['name']}: Polyglot FFI error: {error}")
        if got != case["expected"]:
            raise RuntimeError(
                f"{case['name']}: fixture no longer matches Polyglot FFI\n"
                f"want: {case['expected']}\n got: {got}"
            )

    version = (library.polyglot_version() or b"unknown").decode(
        "utf-8", errors="replace"
    )
    print(
        f"Polyglot FFI {version}: validated {len(cases)} exact-match fixture cases",
        flush=True,
    )
    if args.validate_only:
        return 0
    for case in cases:
        print(
            f"Fixture {case['name']}: {case['source']} -> {case['target']} "
            f"({case['feature']})",
            flush=True,
        )
        subprocess.run(
            [
                str(binary),
                "--fixture",
                case["name"],
                case["source"],
                case["target"],
                case["sql"],
            ],
            check=True,
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
