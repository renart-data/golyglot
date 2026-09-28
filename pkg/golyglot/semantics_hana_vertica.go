package golyglot

import "strings"

func hanaVerticaClockType(name Identifier, dialect Dialect, bare bool) DataTypeKind {
	if name.Quoted {
		return DataTypeUnknown
	}
	upper := strings.ToUpper(name.Text)
	if dialect == DialectHANA {
		switch upper {
		case "CURRENT_UTCTIMESTAMP":
			return DataTypeTimestamp
		case "CURRENT_UTCDATE":
			return DataTypeDate
		case "CURRENT_UTCTIME":
			return DataTypeTime
		}
	}
	if dialect == DialectVertica && (upper == "SYSDATE" || !bare && (upper == "GETDATE" || upper == "GETUTCDATE")) {
		return DataTypeTimestamp
	}
	return DataTypeUnknown
}

func isVerticaDateUnitArgument(function *FunctionCallExpr, index int, dialect Dialect) bool {
	if dialect != DialectVertica || index != 0 || len(function.Name) != 1 || function.Name[0].Quoted || len(function.Args) != 3 {
		return false
	}
	switch strings.ToUpper(function.Name[0].Text) {
	case "TIMESTAMPADD", "TIMESTAMPDIFF", "DATEDIFF":
		id, ok := function.Args[0].(*IdentifierExpr)
		return ok && len(id.Parts) == 1 && !id.Parts[0].Quoted && verticaDateUnit(id.Parts[0].Text) != ""
	}
	return false
}

func verticaDateUnit(unit string) string {
	unit = strings.TrimPrefix(strings.ToUpper(unit), "SQL_TSI_")
	switch unit {
	case "YEAR", "YEARS", "YY", "YYYY":
		return "YEAR"
	case "QUARTER", "QUARTERS", "QQ", "Q":
		return "QUARTER"
	case "MONTH", "MONTHS", "MM", "M":
		return "MONTH"
	case "WEEK", "WEEKS", "WK", "WW":
		return "WEEK"
	case "DAY", "DAYS", "DD", "D", "DAYOFYEAR", "DY", "Y":
		return "DAY"
	case "HOUR", "HOURS", "HH":
		return "HOUR"
	case "MINUTE", "MINUTES", "MI", "N":
		return "MINUTE"
	case "SECOND", "SECONDS", "SS", "S":
		return "SECOND"
	case "MILLISECOND", "MILLISECONDS", "MS":
		return "MILLISECOND"
	case "MICROSECOND", "MICROSECONDS", "US", "MCS":
		return "MICROSECOND"
	}
	return ""
}
