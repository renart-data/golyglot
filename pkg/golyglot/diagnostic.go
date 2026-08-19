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
	// Polyglot is the strict-mode compatibility projection. Diagnostic keeps
	// Golyglot's richer recovery-oriented detail for editor integrations.
	Polyglot PolyglotDiagnostic
}

func (e *SyntaxError) Error() string {
	if e.Polyglot.Kind != PolyglotErrorUnknown {
		return e.Polyglot.Error()
	}
	return e.Diagnostic.Error()
}

func (e *SyntaxError) Unwrap() error { return nil }

// PolyglotErrorKind identifies the public syntax-error variants exposed by
// Polyglot's Rust API.
type PolyglotErrorKind uint8

const (
	PolyglotErrorUnknown PolyglotErrorKind = iota
	PolyglotErrorTokenize
	PolyglotErrorParse
	PolyglotErrorSyntax
)

// PolyglotDiagnostic is the primary diagnostic returned by strict parsing.
// Line and Column are 1-based; Span remains a half-open byte range.
type PolyglotDiagnostic struct {
	Kind    PolyglotErrorKind
	Message string
	Line    int
	Column  int
	Span    Span
}

func (d PolyglotDiagnostic) Error() string {
	prefix := ""
	switch d.Kind {
	case PolyglotErrorTokenize:
		prefix = "Tokenization"
	case PolyglotErrorParse:
		prefix = "Parse"
	case PolyglotErrorSyntax:
		prefix = "Syntax"
	default:
		return d.Message
	}
	return fmt.Sprintf("%s error at line %d, column %d: %s", prefix, d.Line, d.Column, d.Message)
}

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
