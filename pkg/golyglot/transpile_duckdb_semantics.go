package golyglot

import (
	"fmt"
	"strconv"
	"strings"
)

// Expand before target ORDER BY normalization, while explicit source NULLS
// modifiers are still present. In DuckDB the default is NULLS LAST in either
// direction; in PostgreSQL and several other targets DESC defaults to FIRST.
func prepareDuckDBSemantics(root Node, target Dialect) error {
	if target == DialectDuckDB {
		return nil
	}
	scopes := compoundOrderQueries(root)
	var conversionErr error
	Walk(root, func(node Node) VisitAction {
		// Check source types before target cast normalization can erase their
		// width or signedness. Lowering itself happens after normalization.
		if division, ok := node.(*BinaryExpr); ok && division.Operator == "//" {
			if conversionErr = validateDuckDBDivision(division, target); conversionErr != nil {
				return Stop
			}
		}
		query, ok := node.(*SelectStmt)
		if !ok {
			return VisitChildren
		}
		for _, order := range query.OrderBy {
			if !isUnquotedOrderAll(order.Expr) {
				continue
			}
			if len(query.OrderBy) != 1 {
				conversionErr = fmt.Errorf("golyglot: DuckDB ORDER BY ALL cannot be combined with other ordering expressions")
				return Stop
			}
			switch target {
			case DialectGeneric, DialectPostgreSQL, DialectOracle, DialectPresto, DialectTrino, DialectSnowflake, DialectRedshift, DialectSQLite:
			default:
				conversionErr = fmt.Errorf("golyglot: ORDER BY ALL lowering with DuckDB NULL ordering is not verified for %s", target)
				return Stop
			}
			owner := scopes[query]
			if owner == nil {
				owner = query
			}
			width, known := staticProjectionWidth(owner)
			if !known {
				conversionErr = fmt.Errorf("golyglot: cannot expand ORDER BY ALL without a known projection width; expand stars, COLUMNS and schema-dependent projections first")
				return Stop
			}
			query.OrderBy = make([]OrderItem, width)
			for i := range query.OrderBy {
				item := order
				item.Expr = numberExpr(i + 1)
				if !item.NullsFirst && !item.NullsLast && (item.Descending || target == DialectSQLite || target == DialectPresto || target == DialectTrino) {
					item.NullsLast = true
				}
				query.OrderBy[i] = item
			}
			break
		}
		return VisitChildren
	})
	return conversionErr
}

func isUnquotedOrderAll(expr Expr) bool {
	id, ok := expr.(*IdentifierExpr)
	return ok && len(id.Parts) == 1 && !id.Parts[0].Quoted && strings.EqualFold(id.Parts[0].Text, "ALL")
}

func staticProjectionWidth(query *SelectStmt) (int, bool) {
	if query == nil || query.SetModifier != "" && !strings.EqualFold(query.SetModifier, "DISTINCT") {
		return 0, false
	}
	if query.SetLeft != nil {
		return staticProjectionWidth(query.SetLeft)
	}
	for _, item := range query.Projections {
		known := true
		Walk(item.Expr, func(node Node) VisitAction {
			switch value := node.(type) {
			case *StarExpr:
				if node == item.Expr {
					known = false
				}
			case *IdentifierExpr:
				if node == item.Expr && len(value.Parts) > 0 && value.Parts[len(value.Parts)-1].Text == "*" {
					known = false
				}
			case *RawExpr:
				known = false
			case *FunctionCallExpr:
				if len(value.Name) == 1 && (strings.EqualFold(value.Name[0].Text, "COLUMNS") || strings.EqualFold(value.Name[0].Text, "UNNEST")) {
					known = false
				}
			}
			return VisitChildren
		})
		if !known {
			return 0, false
		}
	}
	return len(query.Projections), len(query.Projections) > 0
}

// DuckDB // uses integer division only for integer operands. Decimal/float
// operands use floating-point division (including infinity on division by
// zero), so unconditionally mapping it to PostgreSQL DIV would be incorrect.
// Use native integer / for verified signed integer types, with DuckDB's NULL
// result on a zero divisor, and reject untyped/unsupported conversions.
func lowerDuckDBDivision(root Node, target Dialect) (Node, error) {
	if target == DialectDuckDB {
		return root, nil
	}
	var conversionErr error
	root = Transform(root, func(node Node) Node {
		division, ok := node.(*BinaryExpr)
		if !ok || division.Operator != "//" || conversionErr != nil {
			return node
		}
		if conversionErr = validateDuckDBDivision(division, target); conversionErr != nil {
			return node
		}
		copy := *division
		copy.Operator = "/"
		copy.Right = dialectCall("NULLIF", division.Right, numberExpr(0))
		return &copy
	})
	return root, conversionErr
}

func validateDuckDBDivision(division *BinaryExpr, target Dialect) error {
	if target != DialectPostgreSQL {
		return fmt.Errorf("golyglot: DuckDB // lowering is not verified for %s", target)
	}
	if !knownSignedInteger(division.Left) || !knownSignedInteger(division.Right) {
		return fmt.Errorf("golyglot: cannot safely translate DuckDB // to PostgreSQL without verified signed integer operands; use explicit integer casts")
	}
	return nil
}

func knownSignedInteger(expr Expr) bool {
	switch value := expr.(type) {
	case *LiteralExpr:
		if value.KindValue != LiteralNumber {
			return false
		}
		_, err := strconv.ParseInt(value.Raw, 10, 64)
		return err == nil
	case *UnaryExpr:
		return (value.Operator == "+" || value.Operator == "-") && knownSignedInteger(value.Expr)
	case *ParenthesizedExpr:
		return knownSignedInteger(value.Expr)
	case *CastExpr:
		typeExpr, ok := value.Type.(*IdentifierExpr)
		if !ok || len(typeExpr.Parts) != 1 || typeExpr.Parts[0].Quoted || len(value.TypeSuffix) > 0 {
			return false
		}
		switch strings.ToUpper(typeExpr.Parts[0].Text) {
		case "SMALLINT", "INT2", "INTEGER", "INT", "INT4", "BIGINT", "INT8":
			return true
		}
	case *BinaryExpr:
		return value.Operator == "//" && knownSignedInteger(value.Left) && knownSignedInteger(value.Right)
	}
	return false
}
