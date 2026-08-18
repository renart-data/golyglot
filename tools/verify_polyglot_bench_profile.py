#!/usr/bin/env python3
"""Verify that a Polyglot Cargo manifest retains its fully optimized profile."""

from __future__ import annotations

import argparse
import json
import tomllib
from pathlib import Path


EXPECTED = {
    "inherits": "release",
    "opt-level": 3,
    "lto": True,
    "codegen-units": 1,
}


def verify_profile(path: Path) -> dict[str, object]:
    manifest = tomllib.loads(path.read_text(encoding="utf-8"))
    profile = manifest.get("profile", {}).get("bench", {})
    mismatches = {
        key: profile.get(key)
        for key, expected in EXPECTED.items()
        if profile.get(key) != expected
    }
    if mismatches:
        details = ", ".join(
            f"{key}={value!r} (want {EXPECTED[key]!r})"
            for key, value in mismatches.items()
        )
        raise ValueError(f"Polyglot bench profile is not fully optimized: {details}")
    return EXPECTED.copy()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    args = parser.parse_args()
    try:
        profile = verify_profile(args.manifest)
    except (OSError, ValueError, tomllib.TOMLDecodeError) as error:
        parser.error(str(error))
    print(json.dumps(profile, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
