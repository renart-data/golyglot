package golyglot

import "time"

// An unzoned BigQuery timestamp literal is UTC, whereas DuckDB interprets it
// in the session timezone. Only annotate recognized, unzoned ISO literals;
// explicit offsets/zones and non-literal expressions must remain intact.
func bigQueryDuckDBTimestamp(expression Expr) Expr {
	text := bigQueryDuckDBExprText(expression)
	if literal, ok := expression.(*LiteralExpr); ok && literal.KindValue == LiteralString && len(text) >= 2 && text[0] == '\'' && text[len(text)-1] == '\'' {
		value := text[1 : len(text)-1]
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02"} {
			if _, err := time.Parse(layout, value); err == nil {
				return rawCast("'"+value+" UTC'", "TIMESTAMPTZ")
			}
		}
	}
	return rawCast(text, "TIMESTAMPTZ")
}

// bigQueryDuckDBDate implements DATE(timestamp[, time_zone]) and DATE(datetime).
// DuckDB's TIMESTAMPTZ -> TIMESTAMP cast uses the session timezone, so it must
// not be used to strip the zone before this conversion. EPOCH_US preserves the
// instant of a TIMESTAMPTZ and treats an unzoned TIMESTAMP as UTC. Rebuilding an
// instant at microsecond precision (BigQuery's timestamp precision) therefore
// supports both representations, without duplicating evaluation of the value.
func bigQueryDuckDBDate(args []Expr) Expr {
	// Source transforms run pre-order. Finish nested BigQuery functions before
	// rendering this expression into an opaque target-specific fragment.
	value := normalizeBigQuerySourceNode(args[0], DialectDuckDB).(Expr)
	value = normalizeGenericSourceNode(value, DialectDuckDB).(Expr)
	value = transformExpr(value, DialectDuckDB)
	valueText := renderDialectExpr(value, DialectDuckDB)
	if literal, ok := value.(*LiteralExpr); ok {
		// Give the overloaded epoch function a temporal argument while
		// retaining explicit offsets on implicitly coerced string literals.
		switch literal.KindValue {
		case LiteralString:
			valueText = renderExpr(bigQueryDuckDBTimestamp(literal))
		case LiteralNull:
			valueText = "CAST(" + valueText + " AS TIMESTAMP)"
		}
	}
	zone := "'UTC'"
	if len(args) == 2 {
		zoneExpr := normalizeBigQuerySourceNode(args[1], DialectDuckDB).(Expr)
		zoneExpr = normalizeGenericSourceNode(zoneExpr, DialectDuckDB).(Expr)
		zone = renderDialectExpr(transformExpr(zoneExpr, DialectDuckDB), DialectDuckDB)
	}
	return rawCast("MAKE_TIMESTAMPTZ(EPOCH_US("+valueText+")) AT TIME ZONE "+zone, "DATE")
}
