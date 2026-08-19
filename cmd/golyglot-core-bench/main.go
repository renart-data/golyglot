package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/renart-data/golyglot"
)

const batchSize = 64

var (
	parseSink     golyglot.ParseResult
	transpileSink string
)

type benchmarkOperation struct {
	name   string
	run    func() error
	result func() (string, error)
}

func main() {
	mode := flag.String("mode", "benchmark", "benchmark or result")
	operationName := flag.String("operation", "", "parse or transpile")
	caseName := flag.String("case", "", "benchmark case name")
	sample := flag.Int("sample", 1, "one-based sample number")
	sqlFile := flag.String("sql-file", "", "path to the shared SQL input")
	warmup := flag.Duration("warmup", 250*time.Millisecond, "untimed warmup duration")
	duration := flag.Duration("duration", time.Second, "timed sample duration")
	flag.Parse()

	if *caseName == "" || *sqlFile == "" {
		fatalf("--case and --sql-file are required")
	}
	input, err := os.ReadFile(*sqlFile)
	if err != nil {
		fatalf("read SQL input: %v", err)
	}
	operation, err := newOperation(*operationName, string(input))
	if err != nil {
		fatalf("%v", err)
	}

	switch *mode {
	case "result":
		result, resultErr := operation.result()
		if resultErr != nil {
			fatalf("%s/%s: %v", operation.name, *caseName, resultErr)
		}
		fmt.Print(result)
	case "benchmark":
		if *sample < 1 || *warmup < 0 || *duration <= 0 {
			fatalf("--sample and --duration must be positive; --warmup cannot be negative")
		}
		if *warmup > 0 {
			if _, _, err := runBatches(operation.run, *warmup); err != nil {
				fatalf("warm up %s/%s: %v", operation.name, *caseName, err)
			}
		}
		iterations, elapsed, err := runBatches(operation.run, *duration)
		if err != nil {
			fatalf("benchmark %s/%s: %v", operation.name, *caseName, err)
		}
		nanosecondsPerOperation := float64(elapsed.Nanoseconds()) / float64(iterations)
		fmt.Printf(
			"golyglot\t%s/%s\t%d\t%d\t%d\t%.3f\n",
			operation.name,
			*caseName,
			*sample,
			iterations,
			elapsed.Nanoseconds(),
			nanosecondsPerOperation,
		)
	default:
		fatalf("unsupported --mode %q", *mode)
	}
}

func newOperation(name, sql string) (benchmarkOperation, error) {
	switch name {
	case "parse":
		parse := func() (golyglot.ParseResult, error) {
			result, err := golyglot.ParseStrict(sql, golyglot.DialectGeneric)
			if err != nil {
				return golyglot.ParseResult{}, err
			}
			if len(result.Statements) != 1 {
				return golyglot.ParseResult{}, fmt.Errorf("got %d statements, want 1", len(result.Statements))
			}
			return result, nil
		}
		return benchmarkOperation{
			name: "parse",
			run: func() error {
				result, err := parse()
				parseSink = result
				return err
			},
			result: func() (string, error) {
				result, err := parse()
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("statements=%d\n", len(result.Statements)), nil
			},
		}, nil
	case "transpile":
		transpile := func() (string, error) {
			return golyglot.TranspileOne(sql, golyglot.DialectPostgreSQL, golyglot.DialectMySQL)
		}
		return benchmarkOperation{
			name: "transpile",
			run: func() error {
				result, err := transpile()
				transpileSink = result
				return err
			},
			result: transpile,
		}, nil
	default:
		return benchmarkOperation{}, fmt.Errorf("unsupported --operation %q", name)
	}
}

func runBatches(run func() error, duration time.Duration) (uint64, time.Duration, error) {
	start := time.Now()
	var iterations uint64
	for {
		for range batchSize {
			if err := run(); err != nil {
				return 0, 0, err
			}
		}
		iterations += batchSize
		elapsed := time.Since(start)
		if elapsed >= duration {
			return iterations, elapsed, nil
		}
	}
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "golyglot-core-bench: "+format+"\n", arguments...)
	os.Exit(2)
}
