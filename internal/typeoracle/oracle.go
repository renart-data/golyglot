package typeoracle

import (
	"fmt"
	"strings"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

type Status string

const (
	StatusMatch            Status = "match"
	StatusGolyglotUnknown  Status = "golyglot_unknown"
	StatusTypeMismatch     Status = "type_mismatch"
	StatusModifierMismatch Status = "modifier_mismatch"
	StatusShapeMismatch    Status = "shape_mismatch"
	StatusGolyglotError    Status = "golyglot_error"
	StatusGolyglotCrash    Status = "golyglot_crash"
	StatusGolyglotTimeout  Status = "golyglot_timeout"
	StatusEngineError      Status = "engine_error"
	StatusEngineTimeout    Status = "engine_timeout"
)

type Column struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type QueryCase struct {
	ID      string                     `json:"id"`
	SQL     string                     `json:"sql"`
	Dialect golyglot.Dialect           `json:"dialect"`
	Schema  *golyglot.ValidationSchema `json:"schema"`
}

type Result struct {
	CaseID   string   `json:"caseId"`
	SQL      string   `json:"sql"`
	Status   Status   `json:"status"`
	Detail   string   `json:"detail,omitempty"`
	Inferred []Column `json:"inferred,omitempty"`
	Observed []Column `json:"observed,omitempty"`
}

func Infer(testCase QueryCase) ([]Column, error) {
	analysis, err := golyglot.AnalyzeQuery(testCase.SQL, golyglot.AnalyzeQueryOptions{
		Dialect: testCase.Dialect,
		Schema:  testCase.Schema,
	})
	if err != nil {
		return nil, err
	}
	columns := make([]Column, 0, len(analysis.OutputColumns))
	for _, output := range analysis.OutputColumns {
		column := Column{Name: output.Name}
		if output.TypeHint != nil {
			column.Type = *output.TypeHint
		}
		columns = append(columns, column)
	}
	return columns, nil
}

func Compare(testCase QueryCase, inferred, observed []Column) Result {
	result := Result{CaseID: testCase.ID, SQL: testCase.SQL, Inferred: inferred, Observed: observed}
	if len(inferred) != len(observed) {
		result.Status = StatusShapeMismatch
		result.Detail = fmt.Sprintf("column count: golyglot=%d duckdb=%d", len(inferred), len(observed))
		return result
	}
	for index := range inferred {
		if !strings.EqualFold(inferred[index].Name, observed[index].Name) {
			result.Status = StatusShapeMismatch
			result.Detail = fmt.Sprintf("column %d name: golyglot=%q duckdb=%q", index, inferred[index].Name, observed[index].Name)
			return result
		}
		if strings.TrimSpace(inferred[index].Type) == "" {
			result.Status = StatusGolyglotUnknown
			result.Detail = fmt.Sprintf("column %d (%s) has no inferred type", index, inferred[index].Name)
			return result
		}
		inferredType, err := golyglot.ParseDataType(inferred[index].Type, testCase.Dialect)
		if err != nil || !inferredType.Known() {
			result.Status = StatusGolyglotUnknown
			result.Detail = fmt.Sprintf("column %d inferred type %q is not understood", index, inferred[index].Type)
			return result
		}
		observedType, err := golyglot.ParseDataType(observed[index].Type, testCase.Dialect)
		if err != nil || !observedType.Known() {
			result.Status = StatusEngineError
			result.Detail = fmt.Sprintf("column %d observed type %q is not understood", index, observed[index].Type)
			return result
		}
		if inferredType.SQL() == observedType.SQL() {
			continue
		}
		if inferredType.Kind == observedType.Kind {
			result.Status = StatusModifierMismatch
		} else {
			result.Status = StatusTypeMismatch
		}
		result.Detail = fmt.Sprintf("column %d (%s): golyglot=%s duckdb=%s", index, inferred[index].Name, inferredType.SQL(), observedType.SQL())
		return result
	}
	result.Status = StatusMatch
	return result
}
