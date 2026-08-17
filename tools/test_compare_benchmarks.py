from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from compare_benchmarks import comparison_markdown, read_samples


class CompareBenchmarksTest(unittest.TestCase):
    def test_reads_cpu_suffix_and_reports_medians(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "bench.txt"
            path.write_text(
                "BenchmarkParse/simple-4 100 10 ns/op\n"
                "BenchmarkParse/simple-4 100 14 ns/op\n",
                encoding="utf-8",
            )
            samples = read_samples(path)

        markdown, largest = comparison_markdown(
            samples, {"BenchmarkParse/simple": [9.0, 11.0]}
        )
        self.assertIn("12 ns/op", markdown)
        self.assertIn("10 ns/op", markdown)
        self.assertAlmostEqual(largest, -100 / 6)

    def test_rejects_different_benchmark_sets(self) -> None:
        with self.assertRaisesRegex(ValueError, "benchmark sets differ"):
            comparison_markdown({"BenchmarkA": [1]}, {"BenchmarkB": [1]})


if __name__ == "__main__":
    unittest.main()
