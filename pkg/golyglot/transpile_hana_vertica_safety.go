package golyglot

import (
	"fmt"
	"strconv"
	"strings"
)

// Known vendor extensions must not fall through to similarly named generic
// rewrites. Preserve them for native SQL, but reject unverified conversions.
func validateHANAVerticaConversion(node Node, source, target Dialect) error {
	if source == target {
		return nil
	}
	unsupported := func(feature string) error {
		return fmt.Errorf("golyglot: %s has no verified mapping from %s to %s", feature, source, target)
	}
	switch value := node.(type) {
	case *RawStmt:
		return unsupported("opaque statement " + value.Keyword)
	case *CreateTableStmt:
		if source == DialectHANA || target == DialectHANA || source == DialectVertica {
			// Column constraints, defaults and physical clauses are still an
			// opaque tail. Native round trips are supported, but source DDL
			// must not silently retain vendor types or storage properties.
			return unsupported("native table-definition semantics")
		}
	case *RawExpr, *RawFrom:
		return unsupported("opaque SQL fragment")
	case *TableFunctionFrom:
		if (source == DialectHANA || target == DialectHANA) && len(value.Name) == 1 && !value.Name[0].Quoted {
			return unsupported("HANA table function " + value.Name[0].Text)
		}
	case *SelectStmt:
		if value.RawQuery != "" || value.Tail != "" {
			return unsupported("opaque query extension")
		}
	case *TableName:
		if (source == DialectHANA || source == DialectVertica) && (value.Hint != "" || value.Tail != "") {
			return unsupported("table hint or extension")
		}
	case *IdentifierExpr:
		if (source == DialectHANA || target == DialectHANA) && len(value.Parts) == 1 && !value.Parts[0].Quoted {
			name := strings.ToUpper(value.Parts[0].Text)
			if hanaVerticaClockType(value.Parts[0], DialectHANA, true) != DataTypeUnknown || name == "CURRENT_DATE" || name == "CURRENT_TIME" || name == "CURRENT_TIMESTAMP" {
				return unsupported("HANA current date/time semantics")
			}
		}
	case *CastExpr:
		if source == DialectHANA || target == DialectHANA {
			// Precision, range and conversion failures differ even for types
			// whose spelling resembles another engine's native type.
			return unsupported("HANA CAST conversion semantics")
		}
	case *FunctionCallExpr:
		if len(value.Name) != 1 || value.Name[0].Quoted {
			return nil // Explicitly qualified/quoted user-defined functions.
		}
		name := strings.ToUpper(value.Name[0].Text)
		if target == DialectHANA || target == DialectVertica {
			if name == "STRING_AGG" || name == "GROUP_CONCAT" || name == "LISTAGG" {
				return unsupported("string aggregation NULL/overflow semantics")
			}
		}
		if source == DialectHANA || target == DialectHANA {
			switch name {
			case "COUNT", "SUM", "AVG", "MIN", "MAX", "COALESCE", "NULLIF", "ABS", "UPPER", "LOWER", "ROW_NUMBER", "RANK", "DENSE_RANK":
				return nil
			default:
				return unsupported("HANA function " + name)
			}
		}
		if source == DialectVertica {
			if value.ArgumentTail != "" || value.RawArgs != "" {
				return unsupported("Vertica function parameters")
			}
			switch name {
			case "LISTAGG", "TIME_SLICE", "CONDITIONAL_TRUE_EVENT", "CONDITIONAL_CHANGE_EVENT", "APPROXIMATE_PERCENTILE", "REGEXP_SUBSTR", "REGEXP_REPLACE", "REGEXP_LIKE", "DATEDIFF", "TIMESTAMPDIFF", "TO_DATE", "TO_TIMESTAMP", "TO_CHAR", "TO_NUMBER":
				return unsupported("Vertica function " + name)
			}
		}
	}
	return nil
}

// Keep source defaults explicit for query, aggregate and analytic ordering.
// Vertica source ordering is handled separately because query defaults depend
// on types; HANA uses NULLS FIRST for ASC and NULLS LAST for DESC.
func explicitHANAVerticaOrder(node Node, source Dialect) error {
	var groups [][]OrderItem
	switch value := node.(type) {
	case *SelectStmt:
		groups = append(groups, value.OrderBy, value.SortBy)
		for _, window := range value.Windows {
			groups = append(groups, window.Spec.OrderBy)
		}
	case *FunctionCallExpr:
		groups = append(groups, value.OrderBy, value.WithinGroup)
		if value.Over != nil {
			groups = append(groups, value.Over.OrderBy)
		}
	case *WindowedExpr:
		groups = append(groups, value.Over.OrderBy)
	}
	for _, items := range groups {
		for i := range items {
			item := &items[i]
			if item.NullsFirst || item.NullsLast {
				continue
			}
			switch source {
			case DialectPostgreSQL, DialectOracle, DialectSnowflake, DialectRedshift, DialectCockroachDB, DialectMaterialize, DialectRisingWave:
				item.NullsFirst, item.NullsLast = item.Descending, !item.Descending
			case DialectDuckDB, DialectTrino, DialectPresto, DialectAthena:
				item.NullsLast = true
			case DialectGeneric, DialectHANA, DialectMySQL, DialectSQLite, DialectBigQuery, DialectTSQL, DialectHive, DialectSpark, DialectDatabricks:
				item.NullsFirst, item.NullsLast = !item.Descending, item.Descending
			default:
				return fmt.Errorf("golyglot: add explicit NULLS FIRST/LAST before translating %s ordering to HANA or Vertica", source)
			}
		}
	}
	return nil
}

func verticaAnalyticOrder(items []OrderItem) {
	for i := range items {
		if !items[i].NullsFirst && !items[i].NullsLast {
			items[i].NullsFirst = items[i].Descending
			items[i].NullsLast = !items[i].Descending
		}
	}
}

func verticaSourceOrder(query, scope *SelectStmt) error {
	if scope == nil {
		scope = query
	}
	for i := range query.OrderBy {
		item := &query.OrderBy[i]
		if item.NullsFirst || item.NullsLast {
			continue
		}
		low, known := verticaQueryNullsLow(scope, item.Expr)
		if !known {
			return fmt.Errorf("golyglot: Vertica ORDER BY requires a known, unambiguous sort-key type; add an explicit CAST or NULLS FIRST/LAST")
		}
		item.NullsFirst = low != item.Descending
		item.NullsLast = !item.NullsFirst
	}
	return nil
}

// The parser keeps an unparenthesized compound ORDER BY on the final arm.
// Resolve it against the complete set, as queryScope.bindCompoundOrder does.
func verticaCompoundOrderScopes(root Node) map[*SelectStmt]*SelectStmt {
	result := make(map[*SelectStmt]*SelectStmt)
	Walk(root, func(node Node) VisitAction {
		query, ok := node.(*SelectStmt)
		if !ok || query.SetRight == nil {
			return VisitChildren
		}
		owner := query
		for len(owner.OrderBy) == 0 {
			if owner.SetRight == nil || owner.SetRight.Parenthesized || owner.SetRightParen {
				return VisitChildren
			}
			owner = owner.SetRight
		}
		if result[owner] == nil {
			result[owner] = query
		}
		return VisitChildren
	})
	return result
}

func verticaQueryNullsLow(query *SelectStmt, key Expr) (bool, bool) {
	if query.SetRight != nil {
		if query.SetModifier != "" {
			return false, false
		}
		leftQuery := query.SetLeft
		if leftQuery == nil {
			leftQuery = new(SelectStmt)
			*leftQuery = *query
			leftQuery.SetRight = nil
		}
		left, leftKnown := verticaQueryNullsLow(leftQuery, key)
		right, rightKnown := verticaQueryNullsLow(query.SetRight, key)
		return left, leftKnown && rightKnown && left == right
	}
	if literal, ok := key.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
		position, err := strconv.Atoi(literal.Raw)
		if err != nil || position < 1 || position > len(query.Projections) {
			return false, false
		}
		key = query.Projections[position-1].Expr
	} else if id, ok := key.(*IdentifierExpr); ok && len(id.Parts) == 1 {
		var found Expr
		for _, projection := range query.Projections {
			name := projection.Alias
			if name == nil {
				if column, ok := projection.Expr.(*IdentifierExpr); ok && len(column.Parts) > 0 {
					name = &column.Parts[len(column.Parts)-1]
				}
			}
			if name != nil && identifierKey(*name, DialectVertica) == identifierKey(id.Parts[0], DialectVertica) {
				if found != nil {
					return false, false
				}
				found = projection.Expr
			}
		}
		if found != nil {
			key = found
		}
	}
	return verticaExpressionNullsLow(key)
}

func verticaExpressionNullsLow(key Expr) (bool, bool) {
	var kind DataTypeKind
	switch value := key.(type) {
	case *ParenthesizedExpr:
		return verticaExpressionNullsLow(value.Expr)
	case *CastExpr:
		sql := renderExpr(value.Type)
		for _, suffix := range value.TypeSuffix {
			sql += " " + suffix.Text
		}
		dataType, err := ParseDataType(sql, DialectVertica)
		if err != nil {
			return false, false
		}
		kind = dataType.Kind
	case *TypedLiteralExpr:
		dataType, err := ParseDataType(identifiersText(value.TypeName), DialectVertica)
		if err != nil {
			return false, false
		}
		kind = dataType.Kind
	case *LiteralExpr:
		switch value.KindValue {
		case LiteralNumber:
			if _, err := strconv.ParseInt(value.Raw, 10, 64); err == nil {
				kind = DataTypeBigInt
			} else {
				return false, false
			}
		case LiteralBoolean, LiteralString:
			return false, true
		}
	}
	// Query ORDER BY differs from analytic ORDER BY. See Vertica's
	// documented NULL sort order, including NUMERIC and INTERVAL.
	switch kind {
	case DataTypeTinyInt, DataTypeSmallInt, DataTypeInteger, DataTypeBigInt, DataTypeDecimal, DataTypeDate, DataTypeTime, DataTypeTimestamp, DataTypeInterval:
		return true, true
	case DataTypeFloat, DataTypeDouble, DataTypeString, DataTypeBoolean:
		return false, true
	}
	return false, false
}
