package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/renart-data/golyglot/internal/typeoracle"
)

const workerCommand = "worker"

type reportSummary struct {
	Total      int                       `json:"total"`
	Matches    int                       `json:"matches"`
	Mismatches int                       `json:"mismatches"`
	Unknown    int                       `json:"unknown"`
	Errors     int                       `json:"errors"`
	ByStatus   map[typeoracle.Status]int `json:"byStatus"`
}

type oracleReport struct {
	Seed          int64               `json:"seed"`
	Engine        string              `json:"engine"`
	EngineVersion string              `json:"engineVersion"`
	Summary       reportSummary       `json:"summary"`
	Results       []typeoracle.Result `json:"results"`
}

type workerResponse struct {
	Columns []typeoracle.Column `json:"columns,omitempty"`
	Error   string              `json:"error,omitempty"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == workerCommand {
		os.Exit(runWorker(os.Stdin, os.Stdout))
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("golyglot-type-oracle", flag.ContinueOnError)
	flags.SetOutput(stderr)
	duckDBPath := flags.String("duckdb", "duckdb", "path to the DuckDB CLI")
	seed := flags.Int64("seed", 1, "deterministic generator seed")
	caseCount := flags.Int("cases", 24, "number of generated cases (1-500)")
	caseTimeout := flags.Duration("timeout", 2*time.Second, "timeout for each isolated Golyglot or DuckDB case")
	jsonOutput := flags.Bool("json", false, "emit the complete report as JSON")
	failOnMismatch := flags.Bool("fail-on-mismatch", false, "exit non-zero when the lab finds a mismatch")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *caseCount < 1 || *caseCount > 500 {
		fmt.Fprintln(stderr, "cases must be between 1 and 500")
		return 2
	}
	if *caseTimeout <= 0 {
		fmt.Fprintln(stderr, "timeout must be positive")
		return 2
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "resolve oracle executable: %v\n", err)
		return 2
	}
	adapter := typeoracle.DuckDBAdapter{Executable: *duckDBPath}
	versionContext, cancelVersion := context.WithTimeout(context.Background(), *caseTimeout)
	engineVersion, err := adapter.Version(versionContext)
	cancelVersion()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	cases := typeoracle.GenerateCases(*seed, *caseCount)
	results := make([]typeoracle.Result, 0, len(cases))
	for _, testCase := range cases {
		inferenceContext, cancelInference := context.WithTimeout(context.Background(), *caseTimeout)
		inferred, failureStatus, detail := inferIsolated(inferenceContext, executable, testCase)
		cancelInference()
		if failureStatus != "" {
			results = append(results, typeoracle.Result{CaseID: testCase.ID, SQL: testCase.SQL, Status: failureStatus, Detail: detail})
			continue
		}
		engineContext, cancelEngine := context.WithTimeout(context.Background(), *caseTimeout)
		observed, err := adapter.Describe(engineContext, testCase)
		engineTimedOut := engineContext.Err() == context.DeadlineExceeded
		cancelEngine()
		if err != nil {
			status := typeoracle.StatusEngineError
			if engineTimedOut {
				status = typeoracle.StatusEngineTimeout
			}
			results = append(results, typeoracle.Result{CaseID: testCase.ID, SQL: testCase.SQL, Status: status, Detail: err.Error(), Inferred: inferred})
			continue
		}
		results = append(results, typeoracle.Compare(testCase, inferred, observed))
	}

	report := oracleReport{
		Seed:          *seed,
		Engine:        "duckdb",
		EngineVersion: engineVersion,
		Results:       results,
		Summary:       summarizeResults(results),
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(stderr, "encode report: %v\n", err)
			return 2
		}
	} else {
		writeHumanReport(stdout, report)
	}
	if *failOnMismatch && (report.Summary.Mismatches > 0 || report.Summary.Errors > 0) {
		return 1
	}
	return 0
}

func runWorker(stdin io.Reader, stdout io.Writer) int {
	// A Go stack overflow is process-fatal and cannot be recovered. Keep the
	// worker's stack bounded so generated pathological queries fail cheaply;
	// the parent records the subprocess exit and continues with the next case.
	debug.SetMaxStack(8 << 20)
	debug.SetTraceback("none")
	var testCase typeoracle.QueryCase
	if err := json.NewDecoder(stdin).Decode(&testCase); err != nil {
		_ = json.NewEncoder(stdout).Encode(workerResponse{Error: "decode worker request: " + err.Error()})
		return 2
	}
	columns, err := typeoracle.Infer(testCase)
	response := workerResponse{Columns: columns}
	if err != nil {
		response.Error = err.Error()
	}
	if err := json.NewEncoder(stdout).Encode(response); err != nil {
		return 2
	}
	return 0
}

func inferIsolated(ctx context.Context, executable string, testCase typeoracle.QueryCase) ([]typeoracle.Column, typeoracle.Status, string) {
	payload, err := json.Marshal(testCase)
	if err != nil {
		return nil, typeoracle.StatusGolyglotError, err.Error()
	}
	command := exec.CommandContext(ctx, executable, workerCommand)
	command.Env = append(os.Environ(), "GOTRACEBACK=none")
	command.Stdin = bytes.NewReader(payload)
	var output bytes.Buffer
	failureOutput := &limitedBuffer{remaining: 8 << 10}
	command.Stdout = &output
	command.Stderr = failureOutput
	err = command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, typeoracle.StatusGolyglotTimeout, "isolated inference exceeded its deadline"
	}
	if err != nil {
		detail := strings.TrimSpace(failureOutput.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, typeoracle.StatusGolyglotCrash, detail
	}
	var response workerResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		return nil, typeoracle.StatusGolyglotError, "decode worker response: " + err.Error()
	}
	if response.Error != "" {
		return nil, typeoracle.StatusGolyglotError, response.Error
	}
	return response.Columns, "", ""
}

func summarizeResults(results []typeoracle.Result) reportSummary {
	summary := reportSummary{Total: len(results), ByStatus: make(map[typeoracle.Status]int)}
	for _, result := range results {
		summary.ByStatus[result.Status]++
		switch result.Status {
		case typeoracle.StatusMatch:
			summary.Matches++
		case typeoracle.StatusTypeMismatch, typeoracle.StatusModifierMismatch, typeoracle.StatusShapeMismatch:
			summary.Mismatches++
		case typeoracle.StatusGolyglotUnknown:
			summary.Unknown++
		default:
			summary.Errors++
		}
	}
	return summary
}

func writeHumanReport(output io.Writer, report oracleReport) {
	fmt.Fprintf(output, "Golyglot type oracle vs %s\n", report.EngineVersion)
	fmt.Fprintf(output, "seed=%d cases=%d\n\n", report.Seed, report.Summary.Total)
	for _, result := range report.Results {
		fmt.Fprintf(output, "[%s] %s\n  %s\n", result.Status, result.CaseID, result.SQL)
		if result.Detail != "" {
			fmt.Fprintf(output, "  %s\n", result.Detail)
		}
	}
	fmt.Fprintf(output, "\nsummary: %d match, %d mismatch, %d unknown, %d error\n", report.Summary.Matches, report.Summary.Mismatches, report.Summary.Unknown, report.Summary.Errors)
	statuses := make([]string, 0, len(report.Summary.ByStatus))
	for status, count := range report.Summary.ByStatus {
		statuses = append(statuses, fmt.Sprintf("%s=%d", status, count))
	}
	sort.Strings(statuses)
	fmt.Fprintf(output, "by status: %s\n", strings.Join(statuses, ", "))
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	if buffer.remaining > 0 {
		keep := len(value)
		if keep > buffer.remaining {
			keep = buffer.remaining
		}
		_, _ = buffer.buffer.Write(value[:keep])
		buffer.remaining -= keep
	}
	return written, nil
}

func (buffer *limitedBuffer) String() string {
	return buffer.buffer.String()
}
