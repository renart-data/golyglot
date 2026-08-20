package golyglot

// RecoveryKind identifies syntax retained outside the semantic AST while the
// parser continues through incomplete input. Recovery elements are a sidecar
// produced by the same parser; they do not form a second grammar or tree.
type RecoveryKind uint8

const (
	RecoveryMissing RecoveryKind = iota + 1
	RecoveryUnexpected
	RecoverySkipped
)

// RecoveryElement records a missing or unexpected part of the source. Missing
// elements use a zero-width Span at the insertion point. Unexpected and
// skipped elements cover the original bytes, which remain available through
// ParseResult.Source and ParseResult.Tokens.
type RecoveryElement struct {
	Kind           RecoveryKind
	Span           Span
	Expected       []ExpectedSyntax
	Found          Token
	DiagnosticCode string
}

type recoveryState struct {
	elements []RecoveryElement
}

func recoveryKind(action RecoveryAction) RecoveryKind {
	switch action {
	case RecoveryInserted:
		return RecoveryMissing
	case RecoveryDeleted:
		return RecoveryUnexpected
	case RecoverySynchronized:
		return RecoverySkipped
	default:
		return 0
	}
}

func defaultExpectedSyntax(code string) []ExpectedSyntax {
	switch code {
	case "PARSE_UNCLOSED_PAREN":
		return []ExpectedSyntax{{Kind: ExpectedToken, Text: ")"}}
	case "PARSE_UNCLOSED_BRACKET":
		return []ExpectedSyntax{{Kind: ExpectedToken, Text: "]"}}
	case "PARSE_UNCLOSED_BRACE":
		return []ExpectedSyntax{{Kind: ExpectedToken, Text: "}"}}
	case "PARSE_EXPECTED_IDENTIFIER":
		return []ExpectedSyntax{{Kind: ExpectedIdentifier}}
	case "PARSE_EXPECTED_EXPRESSION":
		return []ExpectedSyntax{{Kind: ExpectedExpression}}
	case "PARSE_EXPECTED_QUERY":
		return []ExpectedSyntax{{Kind: ExpectedQuery}}
	case "PARSE_EXPECTED_TABLE":
		return []ExpectedSyntax{{Kind: ExpectedTable}}
	case "PARSE_INCOMPLETE_STATEMENT":
		return []ExpectedSyntax{{Kind: ExpectedStatement}}
	default:
		return nil
	}
}

func mergeExpectedSyntax(existing, additional []ExpectedSyntax) []ExpectedSyntax {
	for _, candidate := range additional {
		duplicate := false
		for _, current := range existing {
			if current == candidate {
				duplicate = true
				break
			}
		}
		if !duplicate {
			existing = append(existing, candidate)
		}
	}
	return existing
}
