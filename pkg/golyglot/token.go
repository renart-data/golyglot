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

// tokenWord is a compact identity for words repeatedly inspected by parser
// boundary checks. It lives in Token's existing alignment padding, so the
// cache footprint of the token stream does not grow.
type tokenWord uint8

const (
	tokenWordNone tokenWord = iota
	tokenWordSelect
	tokenWordFrom
	tokenWordWhere
	tokenWordGroup
	tokenWordHaving
	tokenWordOrder
	tokenWordLimit
	tokenWordFetch
	tokenWordTableSample
	tokenWordPivot
	tokenWordUnpivot
	tokenWordReplace
	tokenWordUnion
	tokenWordIntersect
	tokenWordExcept
	tokenWordExclude
	tokenWordJoin
	tokenWordStraightJoin
	tokenWordInner
	tokenWordLeft
	tokenWordRight
	tokenWordFull
	tokenWordCross
	tokenWordNatural
	tokenWordOuter
	tokenWordSemi
	tokenWordAnti
	tokenWordOn
	tokenWordUsing
	tokenWordAnd
	tokenWordOr
	tokenWordNot
	tokenWordIn
	tokenWordBetween
	tokenWordIs
	tokenWordLike
	tokenWordILike
	tokenWordAs
	tokenWordFor
	tokenWordWhen
	tokenWordThen
	tokenWordElse
	tokenWordEnd
	tokenWordLateral
	tokenWordConnect
	tokenWordCluster
	tokenWordSample
	tokenWordSettings
	tokenWordMatchRecognize
	tokenWordIndexed
	tokenWordWindow
	tokenWordAt
	tokenWordInto
	tokenWordOffset
	tokenWordQualify
	tokenWordOption
	tokenWordDistribute
	tokenWordSort
	tokenWordMinus
	tokenWordBulk
	tokenWordKeep
	tokenWordFormat
	tokenWordAny
	tokenWordPrewhere
	tokenWordFill
	tokenWordWith
	tokenWordApply
	tokenWordArray
	tokenWordGlobal
	tokenWordAsof
	tokenWordInterpolate
	tokenWordUse
	tokenWordForce
	tokenWordIgnore
	tokenWordPartition
	tokenWordMember
	tokenWordSounds
	tokenWordMod
	tokenWordReturning
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
	word tokenWord
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

type tokenWordDefinition struct {
	text string
	word tokenWord
}

// tokenWordDefinitions is the single mapping between SQL spelling and the
// compact parser identity. The lexer folds it into the same lookup used for
// keyword classification.
var tokenWordDefinitions = [...]tokenWordDefinition{
	{"SELECT", tokenWordSelect},
	{"FROM", tokenWordFrom},
	{"WHERE", tokenWordWhere},
	{"GROUP", tokenWordGroup},
	{"HAVING", tokenWordHaving},
	{"ORDER", tokenWordOrder},
	{"LIMIT", tokenWordLimit},
	{"FETCH", tokenWordFetch},
	{"TABLESAMPLE", tokenWordTableSample},
	{"PIVOT", tokenWordPivot},
	{"UNPIVOT", tokenWordUnpivot},
	{"REPLACE", tokenWordReplace},
	{"UNION", tokenWordUnion},
	{"INTERSECT", tokenWordIntersect},
	{"EXCEPT", tokenWordExcept},
	{"EXCLUDE", tokenWordExclude},
	{"JOIN", tokenWordJoin},
	{"STRAIGHT_JOIN", tokenWordStraightJoin},
	{"INNER", tokenWordInner},
	{"LEFT", tokenWordLeft},
	{"RIGHT", tokenWordRight},
	{"FULL", tokenWordFull},
	{"CROSS", tokenWordCross},
	{"NATURAL", tokenWordNatural},
	{"OUTER", tokenWordOuter},
	{"SEMI", tokenWordSemi},
	{"ANTI", tokenWordAnti},
	{"ON", tokenWordOn},
	{"USING", tokenWordUsing},
	{"AND", tokenWordAnd},
	{"OR", tokenWordOr},
	{"NOT", tokenWordNot},
	{"IN", tokenWordIn},
	{"BETWEEN", tokenWordBetween},
	{"IS", tokenWordIs},
	{"LIKE", tokenWordLike},
	{"ILIKE", tokenWordILike},
	{"AS", tokenWordAs},
	{"FOR", tokenWordFor},
	{"WHEN", tokenWordWhen},
	{"THEN", tokenWordThen},
	{"ELSE", tokenWordElse},
	{"END", tokenWordEnd},
	{"LATERAL", tokenWordLateral},
	{"CONNECT", tokenWordConnect},
	{"CLUSTER", tokenWordCluster},
	{"SAMPLE", tokenWordSample},
	{"SETTINGS", tokenWordSettings},
	{"MATCH_RECOGNIZE", tokenWordMatchRecognize},
	{"INDEXED", tokenWordIndexed},
	{"WINDOW", tokenWordWindow},
	{"AT", tokenWordAt},
	{"INTO", tokenWordInto},
	{"OFFSET", tokenWordOffset},
	{"QUALIFY", tokenWordQualify},
	{"OPTION", tokenWordOption},
	{"DISTRIBUTE", tokenWordDistribute},
	{"SORT", tokenWordSort},
	{"MINUS", tokenWordMinus},
	{"BULK", tokenWordBulk},
	{"KEEP", tokenWordKeep},
	{"FORMAT", tokenWordFormat},
	{"ANY", tokenWordAny},
	{"PREWHERE", tokenWordPrewhere},
	{"FILL", tokenWordFill},
	{"WITH", tokenWordWith},
	{"APPLY", tokenWordApply},
	{"ARRAY", tokenWordArray},
	{"GLOBAL", tokenWordGlobal},
	{"ASOF", tokenWordAsof},
	{"INTERPOLATE", tokenWordInterpolate},
	{"USE", tokenWordUse},
	{"FORCE", tokenWordForce},
	{"IGNORE", tokenWordIgnore},
	{"PARTITION", tokenWordPartition},
	{"MEMBER", tokenWordMember},
	{"SOUNDS", tokenWordSounds},
	{"MOD", tokenWordMod},
	{"RETURNING", tokenWordReturning},
}
