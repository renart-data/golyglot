from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from compare_core_benchmarks import comparison_markdown, read_samples


class CompareCoreBenchmarksTest(unittest.TestCase):
    def test_comparison_includes_speed_and_size(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            golyglot_samples = root / "golyglot.tsv"
            polyglot_samples = root / "polyglot.tsv"
            golyglot_samples.write_text(
                "golyglot\tparse/simple\t1\t10\t1000\t100.0\n"
                "golyglot\tparse/simple\t2\t10\t1200\t120.0\n",
                encoding="utf-8",
            )
            polyglot_samples.write_text(
                "polyglot\tparse/simple\t1\t10\t2000\t200.0\n"
                "polyglot\tparse/simple\t2\t10\t2400\t240.0\n",
                encoding="utf-8",
            )
            golyglot_binary = root / "golyglot"
            polyglot_binary = root / "polyglot"
            golyglot_binary.write_bytes(b"go")
            polyglot_binary.write_bytes(b"rust")

            markdown = comparison_markdown(
                read_samples(golyglot_samples, "golyglot"),
                read_samples(polyglot_samples, "polyglot"),
                2,
                golyglot_binary,
                polyglot_binary,
            )

            self.assertIn("Golyglot 2.00x", markdown)
            self.assertIn("Golyglot is 2.00x smaller", markdown)
            self.assertIn("2/2", markdown)

    def test_rejects_wrong_implementation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            samples = Path(directory) / "samples.tsv"
            samples.write_text(
                "polyglot\tparse/simple\t1\t10\t1000\t100.0\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(ValueError, "expected 'golyglot'"):
                read_samples(samples, "golyglot")


if __name__ == "__main__":
    unittest.main()
