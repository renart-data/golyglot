package golyglot

import "strings"

var aggregateFunctionNames = map[string]struct{}{
	"COUNT": {}, "SUM": {}, "AVG": {}, "MIN": {}, "MAX": {},
	"ARRAY_AGG": {}, "ARRAY_CONCAT_AGG": {}, "STRING_AGG": {}, "GROUP_CONCAT": {}, "LISTAGG": {},
	"STDDEV": {}, "STDDEV_POP": {}, "STDDEV_SAMP": {}, "VARIANCE": {}, "VAR_POP": {}, "VAR_SAMP": {},
	"BOOL_AND": {}, "BOOL_OR": {}, "EVERY": {}, "BIT_AND": {}, "BIT_OR": {}, "BIT_XOR": {},
	"BITWISE_AND_AGG": {}, "BITWISE_OR_AGG": {}, "BITWISE_XOR_AGG": {},
	"CORR": {}, "COVAR_POP": {}, "COVAR_SAMP": {}, "PERCENTILE_CONT": {}, "PERCENTILE_DISC": {},
	"APPROX_COUNT_DISTINCT": {}, "APPROX_DISTINCT": {}, "APPROX_PERCENTILE": {},
	"COLLECT_LIST": {}, "COLLECT_SET": {}, "COUNT_IF": {}, "COUNTIF": {}, "SUM_IF": {}, "SUMIF": {},
	"MEDIAN": {}, "MODE": {}, "FIRST": {}, "LAST": {}, "ANY_VALUE": {}, "FIRST_VALUE": {}, "LAST_VALUE": {},
	"JSON_ARRAYAGG": {}, "JSON_OBJECTAGG": {}, "JSONB_AGG": {}, "JSONB_OBJECT_AGG": {}, "JSON_AGG": {}, "JSON_OBJECT_AGG": {}, "XMLAGG": {},
	"LOGICAL_AND": {}, "LOGICAL_OR": {}, "ARG_MIN": {}, "ARG_MAX": {}, "ARGMIN": {}, "ARGMAX": {}, "MIN_BY": {}, "MAX_BY": {},
	"REGR_SLOPE": {}, "REGR_INTERCEPT": {}, "REGR_COUNT": {}, "REGR_R2": {}, "REGR_AVGX": {}, "REGR_AVGY": {}, "REGR_SXX": {}, "REGR_SYY": {}, "REGR_SXY": {},
	"KURTOSIS": {}, "SKEWNESS": {}, "APPROX_QUANTILES": {}, "APPROX_TOP_COUNT": {}, "ENTROPY": {}, "FAVG": {}, "FSUM": {},
	"RESERVOIR_SAMPLE": {}, "HISTOGRAM": {}, "LIST": {}, "ARBITRARY": {},
}

var duckDBAggregateFunctionNames = map[string]struct{}{
	"ARG_MAX_NULL": {}, "ARG_MIN_NULL": {}, "BITSTRING_AGG": {},
	"SUMKAHAN": {}, "KAHAN_SUM": {}, "GEOMETRIC_MEAN": {}, "GEOMEAN": {},
	"HISTOGRAM_EXACT": {}, "PRODUCT": {}, "WEIGHTED_AVG": {}, "WAVG": {},
	"APPROX_QUANTILE": {}, "RESERVOIR_QUANTILE": {}, "KURTOSIS_POP": {},
	"MAD": {}, "QUANTILE_CONT": {}, "QUANTILE_DISC": {}, "QUANTILE": {}, "SEM": {},
}

func isAggregateFunctionName(name string, dialect Dialect) bool {
	name = strings.ToUpper(strings.TrimSpace(lastIdentifier(name)))
	if _, ok := aggregateFunctionNames[name]; ok {
		return true
	}
	if dialect == DialectDuckDB {
		_, ok := duckDBAggregateFunctionNames[name]
		return ok
	}
	return false
}
