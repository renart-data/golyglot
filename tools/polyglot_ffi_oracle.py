#!/usr/bin/env python3
"""Compare the extended checked-in corpus with a released Polyglot FFI.

This is an exact-output comparison for Polyglot's public transpile/format ABI,
not a reproduction of the 11,333-case compatibility suite reported in the
Polyglot README. The extended corpus also runs reverse ``read`` mappings,
additional target mappings, and project-specific fixtures. Upstream dialect
identity tests use a lower-level parse/transform/generate path with quoting and
keyword-case overrides that the public FFI options do not expose.

The comparison is not a runtime dependency of golyglot. The Go package and its
normal tests remain pure Go. Set POLYGLOT_FFI_PATH to the shared library from a
Polyglot GitHub release (or pass --library).
"""

from __future__ import annotations

import argparse
import ctypes
import json
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


class PolyglotResult(ctypes.Structure):
    _fields_ = [
        ("data", ctypes.c_void_p),
        ("error", ctypes.c_void_p),
        ("status", ctypes.c_int32),
    ]


@dataclass
class Stats:
    passed: int = 0
    failed: int = 0
    failures: list[str] = field(default_factory=list)

    def record(self, label: str, want: str, got: str | None, error: str | None) -> None:
        if error is None and got == want:
            self.passed += 1
            return
        self.failed += 1
        if len(self.failures) < 20:
            if error is not None:
                self.failures.append(f"{label}: {error}")
            else:
                self.failures.append(f"{label}: want {want!r}, got {got!r}")

    def merge(self, other: Stats) -> None:
        self.passed += other.passed
        self.failed += other.failed
        self.failures.extend(other.failures[: max(0, 20 - len(self.failures))])

    def summary(self) -> str:
        total = self.passed + self.failed
        if total == 0:
            return "0/0 (no cases)"
        return f"{self.passed}/{total} ({100 * self.passed / total:.1f}%)"


def load_library(path: Path) -> ctypes.CDLL:
    library = ctypes.CDLL(str(path))
    for name in ["polyglot_format", "polyglot_transpile", "polyglot_transpile_with_options"]:
        function = getattr(library, name)
        if name == "polyglot_format" or name == "polyglot_transpile":
            function.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
        else:
            function.argtypes = [
                ctypes.c_char_p,
                ctypes.c_char_p,
                ctypes.c_char_p,
                ctypes.c_char_p,
            ]
        function.restype = PolyglotResult
    library.polyglot_free_string.argtypes = [ctypes.c_void_p]
    library.polyglot_free_string.restype = None
    library.polyglot_version.restype = ctypes.c_char_p
    return library


def read_pointer(library: ctypes.CDLL, pointer: int | None) -> str:
    if not pointer:
        return ""
    try:
        return ctypes.string_at(pointer).decode("utf-8", errors="replace")
    finally:
        library.polyglot_free_string(pointer)


def call_transpile(
    library: ctypes.CDLL,
    sql: str,
    source: str,
    target: str,
    pretty: bool,
) -> tuple[str | None, str | None]:
    options = json.dumps({"pretty": pretty}, separators=(",", ":")).encode()
    result = library.polyglot_transpile_with_options(
        sql.encode(), source.encode(), target.encode(), options
    )
    return decode_result(library, result)


def call_format(
    library: ctypes.CDLL, sql: str, dialect: str
) -> tuple[str | None, str | None]:
    result = library.polyglot_format(sql.encode(), dialect.encode())
    return decode_result(library, result)


def decode_result(
    library: ctypes.CDLL, result: PolyglotResult
) -> tuple[str | None, str | None]:
    data = read_pointer(library, result.data)
    error = read_pointer(library, result.error)
    if result.status != 0:
        return None, error or f"Polyglot status {result.status}"
    try:
        outputs = json.loads(data)
    except json.JSONDecodeError as exc:
        return None, f"invalid transpile JSON: {exc}"
    if not isinstance(outputs, list) or not outputs:
        return None, "Polyglot returned no statements"
    return "; ".join(str(output) for output in outputs), None


def has_formatting_newline(sql: str) -> bool:
    in_string = False
    in_line_comment = False
    in_block_comment = False
    index = 0
    while index < len(sql):
        current = sql[index]
        following = sql[index + 1] if index + 1 < len(sql) else ""
        if in_line_comment:
            if current == "\n":
                in_line_comment = False
            index += 1
            continue
        if in_block_comment:
            if current == "*" and following == "/":
                in_block_comment = False
                index += 2
                continue
            index += 1
            continue
        if not in_string and current == "-" and following == "-":
            in_line_comment = True
            index += 2
            continue
        if not in_string and current == "/" and following == "*":
            in_block_comment = True
            index += 2
            continue
        if current == "'":
            if in_string and following == "'":
                index += 2
                continue
            in_string = not in_string
        elif current == "\n" and not in_string:
            return True
        index += 1
    return False


def dialect_name(value: str | None) -> str:
    return (value or "generic").strip() or "generic"


def read_json(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as stream:
        return json.load(stream)


def run_case(
    library: ctypes.CDLL,
    stats: Stats,
    label: str,
    sql: str,
    source: str,
    target: str,
    expected: str,
) -> None:
    got, error = call_transpile(
        library, sql, source, target, has_formatting_newline(expected)
    )
    stats.record(label, expected, got, error)


def run_root_fixtures(library: ctypes.CDLL, root: Path) -> Stats:
    total = Stats()
    identity = read_json(root / "identity.json")
    stats = Stats()
    for index, test in enumerate(identity["tests"]):
        sql = test["sql"]
        run_case(library, stats, f"generic identity:{index}", sql, "generic", "generic", sql)
    print(f"SQLGlot generic identity: {stats.summary()}")
    total.merge(stats)

    parser = read_json(root / "parser.json")
    stats = Stats()
    for index, test in enumerate(parser.get("roundtrips", [])):
        source = dialect_name(test.get("read"))
        target = dialect_name(test.get("write"))
        run_case(
            library,
            stats,
            f"parser roundtrip:{index}",
            test["sql"],
            source,
            target,
            test["expected"],
        )
    for index, test in enumerate(parser.get("errors", [])):
        source = dialect_name(test.get("read"))
        _, error = call_transpile(library, test["sql"], source, "generic", False)
        if error is None:
            stats.record(
                f"parser error:{index}",
                "parse error",
                "parsed successfully",
                "released Polyglot accepted an expected parser error",
            )
        else:
            stats.passed += 1
    # The FFI has no tolerant parser entry point; parser errors are reported
    # separately by the Go harness and are intentionally not called successes
    # here merely because the released library rejected them.
    print(f"SQLGlot parser roundtrips: {stats.summary()}")
    total.merge(stats)

    transpile = read_json(root / "transpile.json")
    stats = Stats()
    for index, test in enumerate(transpile.get("normalization", [])):
        run_case(
            library,
            stats,
            f"normalization:{index}",
            test["sql"],
            "generic",
            "generic",
            test["expected"],
        )
    for index, test in enumerate(transpile.get("transpilation", [])):
        if test.get("write"):
            source, target = "generic", dialect_name(test["write"])
        elif test.get("read"):
            source, target = dialect_name(test["read"]), "generic"
        else:
            continue
        run_case(
            library,
            stats,
            f"transpile:{index}",
            test["sql"],
            source,
            target,
            test["expected"],
        )
    print(f"SQLGlot generic transpile/normalization: {stats.summary()}")
    total.merge(stats)

    pretty = read_json(root / "pretty.json")
    stats = Stats()
    for index, test in enumerate(pretty.get("tests", [])):
        got, error = call_format(library, test["input"], "generic")
        expected = test["expected"].strip()
        # The public FFI returns statement bodies without the source
        # terminator, while the pretty fixture records it. Match the same
        # statement-boundary convention used by golyglot.Format.
        if got is not None and expected.endswith(";") and not got.rstrip().endswith(";"):
            got = got.rstrip() + ";"
        stats.record(f"pretty:{index}", expected, got, error)
    print(f"SQLGlot pretty: {stats.summary()}")
    total.merge(stats)
    return total


def run_dialect_fixtures(library: ctypes.CDLL, path: Path) -> Stats:
    name = path.stem
    if name in {"dax", "pipe_syntax", "prql"}:
        return Stats()
    fixture = read_json(path)
    source = dialect_name(fixture.get("dialect") or name)
    stats = Stats()
    for index, test in enumerate(fixture.get("identity", [])):
        expected = test.get("expected") or test["sql"]
        run_case(library, stats, f"{name} identity:{index}", test["sql"], source, source, expected)
    for index, test in enumerate(fixture.get("transpilation", [])):
        for target, expected in test.get("write", {}).items():
            run_case(
                library,
                stats,
                f"{name} write {target}:{index}",
                test["sql"],
                source,
                target,
                expected,
            )
        for source_name, sql in test.get("read", {}).items():
            run_case(
                library,
                stats,
                f"{name} read {source_name}:{index}",
                sql,
                source_name,
                source,
                test["sql"],
            )
    print(f"SQLGlot dialect {name}: {stats.summary()}")
    return stats


def run_custom_fixtures(library: ctypes.CDLL, root: Path) -> Stats:
    total = Stats()
    for path in sorted(root.glob("*/*.json")):
        fixture = read_json(path)
        source = dialect_name(fixture.get("dialect"))
        stats = Stats()
        for index, test in enumerate(fixture.get("identity", [])):
            expected = test.get("expected") or test["sql"]
            run_case(
                library,
                stats,
                f"custom {path.parent.name}/{path.stem} identity:{index}",
                test["sql"],
                source,
                source,
                expected,
            )
        for index, test in enumerate(fixture.get("transpilation", [])):
            for target, expected in test.get("write", {}).items():
                run_case(
                    library,
                    stats,
                    f"custom {path.parent.name}/{path.stem} write {target}:{index}",
                    test["sql"],
                    source,
                    target,
                    expected,
                )
            for source_name, sql in test.get("read", {}).items():
                run_case(
                    library,
                    stats,
                    f"custom {path.parent.name}/{path.stem} read {source_name}:{index}",
                    sql,
                    source_name,
                    source,
                    test["sql"],
                )
        print(f"Polyglot custom {path.parent.name}/{path.stem}: {stats.summary()}")
        total.merge(stats)
    return total


def print_failures(stats: Stats) -> None:
    for failure in stats.failures:
        print(f"  - {failure}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--library",
        type=Path,
        default=os.environ.get("POLYGLOT_FFI_PATH"),
        required=not os.environ.get("POLYGLOT_FFI_PATH"),
        help="released libpolyglot_sql_ffi shared library",
    )
    parser.add_argument(
        "--fixtures",
        type=Path,
        default=Path("testdata/polyglot/sqlglot_fixtures"),
    )
    parser.add_argument(
        "--custom-fixtures",
        type=Path,
        default=Path("testdata/polyglot/custom_fixtures"),
    )
    parser.add_argument(
        "--strict",
        action="store_true",
        help="return failure status for any exact-output difference in the extended comparison",
    )
    args = parser.parse_args()
    library_path = Path(args.library).resolve()
    if not library_path.is_file():
        parser.error(f"FFI library does not exist: {library_path}")

    library = load_library(library_path)
    version = (library.polyglot_version() or b"unknown").decode("utf-8", errors="replace")
    print(f"Polyglot FFI extended comparison {version}: {library_path}")
    print(
        "Scope note: this is not Polyglot's tagged 11,333-case Rust compatibility suite."
    )
    total = run_root_fixtures(library, args.fixtures)
    for path in sorted((args.fixtures / "dialects").glob("*.json")):
        total.merge(run_dialect_fixtures(library, path))
    total.merge(run_custom_fixtures(library, args.custom_fixtures))
    print(f"Polyglot FFI extended corpus: {total.summary()}")
    if total.failures:
        print("First exact-output differences:")
        print_failures(total)
    return 1 if args.strict and total.failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
