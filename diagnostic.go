package golyglot

import "fmt"

type Severity uint8

const (
	SeverityError Severity = iota + 1
	SeverityWarning
	SeverityInformation
	SeverityHint
)

type RecoveryAction uint8

const (
	RecoveryNone RecoveryAction = iota
	RecoveryInserted
	RecoveryDeleted
	RecoverySynchronized
)

// Diagnostic is intentionally close to the useful subset of an LSP
// Diagnostic, while retaining the canonical byte Span used by the parser.
type Diagnostic struct {
	Severity Severity
	Code     string
	Message  string
	Span     Span
	Expected []TokenKind
	Found    TokenKind
	Recovery RecoveryAction
}

func (d Diagnostic) Error() string {
	if d.Code == "" {
		return d.Message
	}
	return fmt.Sprintf("%s: %s", d.Code, d.Message)
}

type SyntaxError struct {
	Diagnostic Diagnostic
}

func (e *SyntaxError) Error() string {
	return e.Diagnostic.Error()
}

func (e *SyntaxError) Unwrap() error { return nil }

type GuardError struct {
	Code    string
	Message string
}

func (e *GuardError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func hasErrorDiagnostics(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}
