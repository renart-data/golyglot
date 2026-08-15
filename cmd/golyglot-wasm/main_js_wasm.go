//go:build js && wasm

package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/tobilg/golyglot"
)

var callbacks []js.Func

type wasmDiagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
}

type wasmResponse struct {
	OK             bool             `json:"ok"`
	Error          string           `json:"error,omitempty"`
	Dialect        string           `json:"dialect,omitempty"`
	Statements     int              `json:"statements"`
	StatementKinds []string         `json:"statementKinds,omitempty"`
	Diagnostics    []wasmDiagnostic `json:"diagnostics"`
	Formatted      string           `json:"formatted,omitempty"`
}

func main() {
	api := js.Global().Get("Object").New()
	for _, method := range []struct {
		name string
		fn   func(js.Value, []js.Value) any
	}{
		{name: "parse", fn: parseSQL},
		{name: "format", fn: formatSQL},
		{name: "transpile", fn: transpileSQL},
	} {
		callback := js.FuncOf(method.fn)
		callbacks = append(callbacks, callback)
		api.Set(method.name, callback)
	}
	js.Global().Set("golyglot", api)

	// Keep the Go runtime and callback values alive for the lifetime of the
	// page. The JavaScript caller owns the editor and decides when to reload the
	// WASM instance.
	select {}
}

func parseSQL(_ js.Value, args []js.Value) any {
	sql, dialect, err := requestArgs(args, 2)
	if err != nil {
		return encode(wasmResponse{Error: err.Error()})
	}
	result := golyglot.ParseTolerant(sql, dialect)
	response := wasmResponse{
		OK:          !result.HasErrors(),
		Dialect:     string(dialect),
		Statements:  len(result.Statements),
		Diagnostics: diagnostics(result.Diagnostics),
	}
	for _, statement := range result.Statements {
		response.StatementKinds = append(response.StatementKinds, string(statement.Node.Kind()))
		if response.Formatted == "" {
			response.Formatted, _ = golyglot.GenerateWithOptions(statement.Node, golyglot.GenerateOptions{
				Pretty:    true,
				Canonical: true,
				Dialect:   dialect,
			})
		}
	}
	return encode(response)
}

func formatSQL(_ js.Value, args []js.Value) any {
	sql, dialect, err := requestArgs(args, 2)
	if err != nil {
		return encode(wasmResponse{Error: err.Error()})
	}
	formatted, err := golyglot.FormatOne(sql, dialect)
	if err != nil {
		return encode(wasmResponse{Error: err.Error(), Dialect: string(dialect)})
	}
	return encode(wasmResponse{OK: true, Dialect: string(dialect), Statements: 1, Formatted: formatted})
}

func transpileSQL(_ js.Value, args []js.Value) any {
	if len(args) < 3 {
		return encode(wasmResponse{Error: "transpile expects SQL, source dialect, and target dialect"})
	}
	sql := args[0].String()
	from, err := golyglot.ParseDialect(args[1].String())
	if err != nil {
		return encode(wasmResponse{Error: err.Error()})
	}
	to, err := golyglot.ParseDialect(args[2].String())
	if err != nil {
		return encode(wasmResponse{Error: err.Error()})
	}
	transpiled, err := golyglot.TranspileOne(sql, from, to)
	if err != nil {
		return encode(wasmResponse{Error: err.Error(), Dialect: string(to)})
	}
	return encode(wasmResponse{OK: true, Dialect: string(to), Statements: 1, Formatted: transpiled})
}

func requestArgs(args []js.Value, count int) (string, golyglot.Dialect, error) {
	if len(args) < count {
		return "", golyglot.DialectGeneric, fmt.Errorf("expected SQL and dialect")
	}
	dialect, err := golyglot.ParseDialect(args[1].String())
	if err != nil {
		return "", golyglot.DialectGeneric, err
	}
	return args[0].String(), dialect, nil
}

func diagnostics(values []golyglot.Diagnostic) []wasmDiagnostic {
	result := make([]wasmDiagnostic, 0, len(values))
	for _, value := range values {
		result = append(result, wasmDiagnostic{
			Severity: severityName(value.Severity),
			Code:     value.Code,
			Message:  value.Message,
			Start:    value.Span.Start,
			End:      value.Span.End,
		})
	}
	return result
}

func severityName(value golyglot.Severity) string {
	switch value {
	case golyglot.SeverityWarning:
		return "warning"
	case golyglot.SeverityInformation:
		return "information"
	case golyglot.SeverityHint:
		return "hint"
	default:
		return "error"
	}
}

func encode(value wasmResponse) js.Value {
	data, err := json.Marshal(value)
	if err != nil {
		data, _ = json.Marshal(wasmResponse{Error: err.Error()})
	}
	return js.ValueOf(string(data))
}
