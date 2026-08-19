package golyglot

import (
	"sort"
	"unicode/utf8"
)

// Span is a half-open byte range into the original SQL string.
type Span struct {
	Start int
	End   int
}

func (s Span) Empty() bool { return s.Start == s.End }

// IsSynthetic reports whether a node has no direct origin in the input. The
// parser uses this sentinel for semantic nodes introduced while normalizing
// source syntax; it is never a valid TextEdit range.
func (s Span) IsSynthetic() bool { return s.Start == -1 && s.End == -1 }

func (s Span) Valid(textLen int) bool {
	return s.Start >= 0 && s.End >= s.Start && s.End <= textLen
}

func syntheticSpan() Span { return Span{Start: -1, End: -1} }

func mergeSpans(first, last Span) Span {
	return Span{Start: first.Start, End: last.End}
}

// PositionEncoding is the character unit used when converting byte spans to
// editor positions. LSP clients commonly negotiate UTF-16, but UTF-8 and
// UTF-32 are supported too.
type PositionEncoding uint8

const (
	PositionUTF8 PositionEncoding = iota
	PositionUTF16
	PositionUTF32
)

// Position is zero-based. Character is measured according to the requested
// PositionEncoding.
type Position struct {
	Line      int
	Character int
}

type Range struct {
	Start Position
	End   Position
}

// SourceText maps byte offsets to line and editor positions without requiring
// an LSP dependency in the parser core.
type SourceText struct {
	text       string
	lineStarts []int
}

func NewSourceText(text string) SourceText {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return SourceText{text: text, lineStarts: starts}
}

func (s SourceText) Text() string { return s.text }

func (s SourceText) PositionAt(byteOffset int, encoding PositionEncoding) Position {
	if byteOffset < 0 {
		byteOffset = 0
	}
	if byteOffset > len(s.text) {
		byteOffset = len(s.text)
	}
	line := sort.Search(len(s.lineStarts), func(i int) bool {
		return s.lineStarts[i] > byteOffset
	}) - 1
	if line < 0 {
		line = 0
	}
	lineStart := s.lineStarts[line]
	return Position{Line: line, Character: characterWidth(s.text[lineStart:byteOffset], encoding)}
}

func (s SourceText) Range(span Span, encoding PositionEncoding) Range {
	return Range{
		Start: s.PositionAt(span.Start, encoding),
		End:   s.PositionAt(span.End, encoding),
	}
}

func characterWidth(text string, encoding PositionEncoding) int {
	switch encoding {
	case PositionUTF8:
		return len(text)
	case PositionUTF32:
		return utf8.RuneCountInString(text)
	case PositionUTF16:
		width := 0
		for len(text) > 0 {
			r, size := utf8.DecodeRuneInString(text)
			if r > 0xffff {
				width += 2
			} else {
				width++
			}
			text = text[size:]
		}
		return width
	default:
		return len(text)
	}
}
