package golyglot

import (
	"fmt"
	"sort"
	"strings"
)

// TextEdit replaces a half-open byte span in a parsed SQL document.
// A zero-width span inserts NewText at that byte offset.
type TextEdit struct {
	Span    Span
	NewText string
}

// OriginalSQL returns the input bytes exactly as they were passed to Parse.
// It is the lossless representation of the document; formatting and AST
// generation are intentionally separate operations.
func (r ParseResult) OriginalSQL() string { return r.SQL }

// SourceSlice returns the exact source bytes covered by span. The boolean is
// false when span is not a valid byte range in the original document.
func (r ParseResult) SourceSlice(span Span) (string, bool) {
	if !span.Valid(len(r.SQL)) {
		return "", false
	}
	return r.SQL[span.Start:span.End], true
}

// SourceGapBefore returns the source bytes not represented by a token between
// the previous token and tokenIndex. The lexer emits comments as tokens, so
// these gaps normally contain whitespace. The gap before the EOF token holds
// trailing whitespace.
//
// Iterating Tokens in order and concatenating each gap with each token's
// SourceSlice reconstructs OriginalSQL exactly (the EOF token itself is
// zero-width).
func (r ParseResult) SourceGapBefore(tokenIndex int) (Span, bool) {
	if tokenIndex < 0 || tokenIndex >= len(r.Tokens) {
		return Span{}, false
	}
	start := 0
	if tokenIndex > 0 {
		previous := r.Tokens[tokenIndex-1].Span
		if !previous.Valid(len(r.SQL)) {
			return Span{}, false
		}
		start = previous.End
	}
	current := r.Tokens[tokenIndex].Span
	if !current.Valid(len(r.SQL)) || current.Start < start {
		return Span{}, false
	}
	return Span{Start: start, End: current.Start}, true
}

// EditForNode creates a source edit for a parsed node. Synthetic and
// zero-width recovery nodes cannot be edited because they do not own source
// bytes; callers can still create an insertion with TextEdit directly.
func (r ParseResult) EditForNode(node Node, newText string) (TextEdit, error) {
	if node == nil {
		return TextEdit{}, fmt.Errorf("cannot edit a nil node")
	}
	span := node.SourceSpan()
	if span.IsSynthetic() {
		return TextEdit{}, fmt.Errorf("cannot edit synthetic %s node", node.Kind())
	}
	if !span.Valid(len(r.SQL)) {
		return TextEdit{}, fmt.Errorf("%s node has invalid span [%d,%d) for %d-byte SQL", node.Kind(), span.Start, span.End, len(r.SQL))
	}
	if span.Empty() {
		return TextEdit{}, fmt.Errorf("cannot replace zero-width %s recovery node", node.Kind())
	}
	return TextEdit{Span: span, NewText: newText}, nil
}

// ApplyEdits applies source edits without parsing or regenerating unaffected
// text. Edits may be supplied in any order, but their spans must not overlap.
// Insertions at the same byte offset retain their caller-supplied order.
// Passing no edits returns OriginalSQL without allocating.
func (r ParseResult) ApplyEdits(edits ...TextEdit) (string, error) {
	if len(edits) == 0 {
		return r.SQL, nil
	}

	ordered := append([]TextEdit(nil), edits...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Span.Start != ordered[j].Span.Start {
			return ordered[i].Span.Start < ordered[j].Span.Start
		}
		// Insertions at the start of a replacement are applied before it.
		if ordered[i].Span.Empty() != ordered[j].Span.Empty() {
			return ordered[i].Span.Empty()
		}
		return false
	})

	outputLength := len(r.SQL)
	cursor := 0
	unchanged := true
	for i, edit := range ordered {
		if !edit.Span.Valid(len(r.SQL)) {
			return "", fmt.Errorf("text edit %d has invalid span [%d,%d) for %d-byte SQL", i, edit.Span.Start, edit.Span.End, len(r.SQL))
		}
		if edit.Span.Start < cursor {
			return "", fmt.Errorf("text edit %d overlaps an earlier edit at byte %d", i, edit.Span.Start)
		}
		removed := edit.Span.End - edit.Span.Start
		outputLength -= removed
		if len(edit.NewText) > maxIntValue-outputLength {
			return "", fmt.Errorf("text edits produce output too large to represent")
		}
		outputLength += len(edit.NewText)
		cursor = edit.Span.End
		if edit.NewText != r.SQL[edit.Span.Start:edit.Span.End] {
			unchanged = false
		}
	}
	if unchanged {
		return r.SQL, nil
	}

	var output strings.Builder
	output.Grow(outputLength)
	cursor = 0
	for _, edit := range ordered {
		output.WriteString(r.SQL[cursor:edit.Span.Start])
		output.WriteString(edit.NewText)
		cursor = edit.Span.End
	}
	output.WriteString(r.SQL[cursor:])
	return output.String(), nil
}

const maxIntValue = int(^uint(0) >> 1)
