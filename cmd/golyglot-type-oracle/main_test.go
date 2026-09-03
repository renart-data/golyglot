package main

import (
	"testing"

	"github.com/renart-data/golyglot/internal/typeoracle"
)

func TestSummarizeResults(t *testing.T) {
	summary := summarizeResults([]typeoracle.Result{
		{Status: typeoracle.StatusMatch},
		{Status: typeoracle.StatusTypeMismatch},
		{Status: typeoracle.StatusModifierMismatch},
		{Status: typeoracle.StatusGolyglotUnknown},
		{Status: typeoracle.StatusGolyglotCrash},
	})
	if summary.Total != 5 || summary.Matches != 1 || summary.Mismatches != 2 || summary.Unknown != 1 || summary.Errors != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}
