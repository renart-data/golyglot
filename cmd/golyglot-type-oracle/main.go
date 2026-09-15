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
	"path/filepath"
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
	ByFeature  map[string]resultCounts   `json:"byFeature"`
	Expected   int                       `json:"expected"`
	Unexpected int                       `json:"unexpected"`
}

type resultCounts struct {
	Total      int                       `json:"total"`
	Matches    int                       `json:"matches"`
	Mismatches int                       `json:"mismatches"`
	Unknown    int                       `json:"unknown"`
	Errors     int                       `json:"errors"`
	ByStatus   map[typeoracle.Status]int `json:"byStatus"`
}

type oracleReport struct {
	Seed             int64               `json:"seed"`
	GeneratorVersion string              `json:"generatorVersion"`
	Engine           string              `json:"engine"`
	EngineVersion    string              `json:"engineVersion"`
	Summary          reportSummary       `json:"summary"`
	Results          []typeoracle.Result `json:"results"`
}

type findingBundle struct {
	Version          string               `json:"version"`
	Seed             int64                `json:"seed"`
	GeneratorVersion string               `json:"generatorVersion"`
	Engine           string               `json:"engine"`
	EngineVersion    string               `json:"engineVersion"`
	Findings         []typeoracle.Finding `json:"findings"`
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
	corpus := flags.String("corpus", "generated", "case corpus: generated, curated, or all")
	caseTimeout := flags.Duration("timeout", 2*time.Second, "timeout for each isolated Golyglot or DuckDB case")
	jsonOutput := flags.Bool("json", false, "emit the complete report as JSON")
	findingsPath := flags.String("write-findings", "", "atomically write minimized non-match reproductions to this JSON file")
	failOnMismatch := flags.Bool("fail-on-mismatch", false, "exit non-zero when the lab finds a mismatch")
	failOnUnexpected := flags.Bool("fail-on-unexpected", false, "exit non-zero for a finding not matched by a pinned expectation")
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

	cases, err := oracleCases(*corpus, *seed, *caseCount)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	results := make([]typeoracle.Result, 0, len(cases))
	for _, testCase := range cases {
		inferenceContext, cancelInference := context.WithTimeout(context.Background(), *caseTimeout)
		inferred, failureStatus, detail := inferIsolated(inferenceContext, executable, testCase)
		cancelInference()
		if failureStatus != "" {
			result := typeoracle.NewResult(testCase, failureStatus, detail)
			result.Expectation = typeoracle.EvaluateExpectation(result, testCase, "duckdb", engineVersion)
			results = append(results, result)
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
			result := typeoracle.NewResult(testCase, status, err.Error())
			result.Inferred = inferred
			result.Expectation = typeoracle.EvaluateExpectation(result, testCase, "duckdb", engineVersion)
			results = append(results, result)
			continue
		}
		result := typeoracle.Compare(testCase, inferred, observed)
		result.Expectation = typeoracle.EvaluateExpectation(result, testCase, "duckdb", engineVersion)
		results = append(results, result)
	}

	report := oracleReport{
		Seed:             *seed,
		GeneratorVersion: typeoracle.GeneratorVersion,
		Engine:           "duckdb",
		EngineVersion:    engineVersion,
		Results:          results,
		Summary:          summarizeResults(results),
	}
	if strings.TrimSpace(*findingsPath) != "" {
		if err := writeFindings(*findingsPath, report, cases); err != nil {
			fmt.Fprintf(stderr, "write findings: %v\n", err)
			return 2
		}
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
	if *failOnUnexpected && report.Summary.Unexpected > 0 {
		return 1
	}
	return 0
}

func writeFindings(path string, report oracleReport, cases []typeoracle.QueryCase) error {
	if len(report.Results) != len(cases) {
		return fmt.Errorf("result count %d does not match case count %d", len(report.Results), len(cases))
	}
	bundle := findingBundle{
		Version: typeoracle.FindingVersion, Seed: report.Seed,
		GeneratorVersion: report.GeneratorVersion, Engine: report.Engine,
		EngineVersion: report.EngineVersion,
	}
	for index, result := range report.Results {
		if !isFinding(result) {
			continue
		}
		finding, err := typeoracle.NewFinding(report.Seed, report.Engine, report.EngineVersion, cases[index], result)
		if err != nil {
			return fmt.Errorf("reduce %s: %w", result.CaseID, err)
		}
		bundle.Findings = append(bundle.Findings, finding)
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path = filepath.Clean(path)
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".golyglot-findings-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func isFinding(result typeoracle.Result) bool {
	if result.Expectation != nil && result.Expectation.Applicable {
		return !result.Expectation.Matched
	}
	return result.Status != typeoracle.StatusMatch
}

func oracleCases(corpus string, seed int64, count int) ([]typeoracle.QueryCase, error) {
	switch strings.ToLower(strings.TrimSpace(corpus)) {
	case "generated":
		return typeoracle.GenerateCases(seed, count), nil
	case "curated":
		return typeoracle.CuratedCases()
	case "all":
		curated, err := typeoracle.CuratedCases()
		if err != nil {
			return nil, err
		}
		return append(typeoracle.GenerateCases(seed, count), curated...), nil
	default:
		return nil, fmt.Errorf("corpus must be generated, curated, or all")
	}
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
	summary := reportSummary{
		Total: len(results), ByStatus: make(map[typeoracle.Status]int),
		ByFeature: make(map[string]resultCounts),
	}
	for _, result := range results {
		summary.ByStatus[result.Status]++
		matches, mismatches, unknown, errors := resultCategories(result.Status)
		summary.Matches += matches
		summary.Mismatches += mismatches
		summary.Unknown += unknown
		summary.Errors += errors
		if result.Expectation != nil && result.Expectation.Applicable {
			if result.Expectation.Matched {
				if result.Status != typeoracle.StatusMatch {
					summary.Expected++
				}
			} else {
				summary.Unexpected++
			}
		} else if result.Status != typeoracle.StatusMatch {
			summary.Unexpected++
		}
		seenFeatures := make(map[string]struct{}, len(result.Features))
		for _, feature := range result.Features {
			if _, ok := seenFeatures[feature]; ok {
				continue
			}
			seenFeatures[feature] = struct{}{}
			counts := summary.ByFeature[feature]
			if counts.ByStatus == nil {
				counts.ByStatus = make(map[typeoracle.Status]int)
			}
			counts.Total++
			counts.Matches += matches
			counts.Mismatches += mismatches
			counts.Unknown += unknown
			counts.Errors += errors
			counts.ByStatus[result.Status]++
			summary.ByFeature[feature] = counts
		}
	}
	return summary
}

func resultCategories(status typeoracle.Status) (matches, mismatches, unknown, errors int) {
	switch status {
	case typeoracle.StatusMatch:
		matches = 1
	case typeoracle.StatusTypeMismatch, typeoracle.StatusModifierMismatch, typeoracle.StatusShapeMismatch:
		mismatches = 1
	case typeoracle.StatusGolyglotUnknown:
		unknown = 1
	default:
		errors = 1
	}
	return
}

func writeHumanReport(output io.Writer, report oracleReport) {
	fmt.Fprintf(output, "Golyglot type oracle vs %s\n", report.EngineVersion)
	fmt.Fprintf(output, "seed=%d cases=%d\n\n", report.Seed, report.Summary.Total)
	for _, result := range report.Results {
		expectation := ""
		if result.Expectation != nil && result.Expectation.Applicable && result.Expectation.Matched {
			expectation = " pinned"
		} else if result.Expectation != nil && result.Expectation.Applicable {
			expectation = " expectation-drift"
		}
		fmt.Fprintf(output, "[%s%s] %s\n  %s\n", result.Status, expectation, result.CaseID, result.SQL)
		if len(result.Features) > 0 {
			fmt.Fprintf(output, "  features: %s\n", strings.Join(result.Features, ", "))
		}
		if result.Detail != "" {
			fmt.Fprintf(output, "  %s\n", result.Detail)
		}
	}
	fmt.Fprintf(output, "\nsummary: %d match, %d mismatch, %d unknown, %d error; %d expected, %d unexpected finding\n", report.Summary.Matches, report.Summary.Mismatches, report.Summary.Unknown, report.Summary.Errors, report.Summary.Expected, report.Summary.Unexpected)
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
