package golyglot

import "fmt"

type TokenKind uint8

const (
	TokenEOF TokenKind = iota
	TokenIdentifier
	TokenQuotedIdentifier
	TokenKeyword
	TokenString
	TokenNumber
	TokenParameter
	TokenOperator
	TokenPunctuation
	TokenComment
	TokenUnknown
	TokenUnterminatedString
	TokenUnterminatedComment
)

func (k TokenKind) String() string {
	switch k {
	case TokenEOF:
		return "end of input"
	case TokenIdentifier:
		return "identifier"
	case TokenQuotedIdentifier:
		return "quoted identifier"
	case TokenKeyword:
		return "keyword"
	case TokenString:
		return "string literal"
	case TokenNumber:
		return "number"
	case TokenParameter:
		return "parameter"
	case TokenOperator:
		return "operator"
	case TokenPunctuation:
		return "punctuation"
	case TokenComment:
		return "comment"
	case TokenUnknown:
		return "unknown token"
	case TokenUnterminatedString:
		return "unterminated string literal"
	case TokenUnterminatedComment:
		return "unterminated comment"
	default:
		return fmt.Sprintf("token kind %d", k)
	}
}

// Token retains the original source text. Comments are emitted rather than
// discarded so a future generator can preserve identity and trivia.
type Token struct {
	Kind TokenKind
	Text string
	Span Span
}

func (t Token) IsWord(word string) bool {
	return (t.Kind == TokenIdentifier || t.Kind == TokenKeyword) && equalFoldASCII(t.Text, word)
}

func (t Token) Description() string {
	if t.Kind == TokenEOF {
		return "end of input"
	}
	if t.Text == "" {
		return t.Kind.String()
	}
	return fmt.Sprintf("%q", t.Text)
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'a' && ca <= 'z' {
			ca -= 'a' - 'A'
		}
		if cb >= 'a' && cb <= 'z' {
			cb -= 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
