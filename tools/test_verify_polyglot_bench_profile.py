from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from verify_polyglot_bench_profile import verify_profile


class VerifyPolyglotBenchProfileTest(unittest.TestCase):
    def test_accepts_full_optimization(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            manifest = Path(directory) / "Cargo.toml"
            manifest.write_text(
                "[profile.bench]\n"
                'inherits = "release"\n'
                "opt-level = 3\n"
                "lto = true\n"
                "codegen-units = 1\n",
                encoding="utf-8",
            )
            profile = verify_profile(manifest)
            self.assertEqual(profile["inherits"], "release")
            self.assertEqual(profile["lto"], True)

    def test_rejects_memory_saving_overrides(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            manifest = Path(directory) / "Cargo.toml"
            manifest.write_text(
                "[profile.bench]\n"
                'inherits = "release"\n'
                "opt-level = 3\n"
                "lto = false\n"
                "codegen-units = 16\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(ValueError, "not fully optimized"):
                verify_profile(manifest)


if __name__ == "__main__":
    unittest.main()
