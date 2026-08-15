package golyglot

import (
	"fmt"
	"strings"
)

// Dialect identifies the SQL vocabulary and grammar extensions used while
// parsing. Dialects are strings so callers can persist them without depending
// on numeric enum values.
type Dialect string

const (
	DialectGeneric     Dialect = "generic"
	DialectPostgreSQL  Dialect = "postgresql"
	DialectMySQL       Dialect = "mysql"
	DialectBigQuery    Dialect = "bigquery"
	DialectSnowflake   Dialect = "snowflake"
	DialectDuckDB      Dialect = "duckdb"
	DialectSQLite      Dialect = "sqlite"
	DialectHive        Dialect = "hive"
	DialectSpark       Dialect = "spark"
	DialectTrino       Dialect = "trino"
	DialectPresto      Dialect = "presto"
	DialectRedshift    Dialect = "redshift"
	DialectTSQL        Dialect = "tsql"
	DialectOracle      Dialect = "oracle"
	DialectClickHouse  Dialect = "clickhouse"
	DialectDatabricks  Dialect = "databricks"
	DialectAthena      Dialect = "athena"
	DialectTeradata    Dialect = "teradata"
	DialectDoris       Dialect = "doris"
	DialectStarRocks   Dialect = "starrocks"
	DialectMaterialize Dialect = "materialize"
	DialectRisingWave  Dialect = "risingwave"
	DialectSingleStore Dialect = "singlestore"
	DialectCockroachDB Dialect = "cockroachdb"
	DialectTiDB        Dialect = "tidb"
	DialectDruid       Dialect = "druid"
	DialectSolr        Dialect = "solr"
	DialectTableau     Dialect = "tableau"
	DialectDune        Dialect = "dune"
	DialectFabric      Dialect = "fabric"
	DialectDrill       Dialect = "drill"
	DialectDremio      Dialect = "dremio"
	DialectExasol      Dialect = "exasol"
	DialectDataFusion  Dialect = "datafusion"
)

// ParseDialect accepts Polyglot-style names and common aliases.
func ParseDialect(name string) (Dialect, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch normalized {
	case "", "generic", "ansi":
		return DialectGeneric, nil
	case "postgres", "postgresql", "pgsql":
		return DialectPostgreSQL, nil
	case "mysql", "mariadb":
		return DialectMySQL, nil
	case "bigquery", "big_query":
		return DialectBigQuery, nil
	case "snowflake":
		return DialectSnowflake, nil
	case "duckdb":
		return DialectDuckDB, nil
	case "sqlite", "sqlite3":
		return DialectSQLite, nil
	case "hive":
		return DialectHive, nil
	case "spark", "spark2":
		return DialectSpark, nil
	case "trino":
		return DialectTrino, nil
	case "presto":
		return DialectPresto, nil
	case "redshift":
		return DialectRedshift, nil
	case "tsql", "mssql", "sqlserver":
		return DialectTSQL, nil
	case "oracle":
		return DialectOracle, nil
	case "clickhouse":
		return DialectClickHouse, nil
	case "databricks":
		return DialectDatabricks, nil
	case "athena":
		return DialectAthena, nil
	case "teradata":
		return DialectTeradata, nil
	case "doris":
		return DialectDoris, nil
	case "starrocks", "star_rocks":
		return DialectStarRocks, nil
	case "materialize":
		return DialectMaterialize, nil
	case "risingwave":
		return DialectRisingWave, nil
	case "singlestore", "single_store", "memsql":
		return DialectSingleStore, nil
	case "cockroach", "cockroachdb":
		return DialectCockroachDB, nil
	case "tidb":
		return DialectTiDB, nil
	case "druid":
		return DialectDruid, nil
	case "solr":
		return DialectSolr, nil
	case "tableau":
		return DialectTableau, nil
	case "dune":
		return DialectDune, nil
	case "fabric":
		return DialectFabric, nil
	case "drill":
		return DialectDrill, nil
	case "dremio":
		return DialectDremio, nil
	case "exasol":
		return DialectExasol, nil
	case "datafusion", "data_fusion":
		return DialectDataFusion, nil
	default:
		return "", fmt.Errorf("unknown SQL dialect %q", name)
	}
}

func (d Dialect) normalized() (Dialect, error) {
	return ParseDialect(string(d))
}

var supportedDialects = []Dialect{
	DialectGeneric,
	DialectPostgreSQL,
	DialectMySQL,
	DialectBigQuery,
	DialectSnowflake,
	DialectDuckDB,
	DialectSQLite,
	DialectHive,
	DialectSpark,
	DialectTrino,
	DialectPresto,
	DialectRedshift,
	DialectTSQL,
	DialectOracle,
	DialectClickHouse,
	DialectDatabricks,
	DialectAthena,
	DialectTeradata,
	DialectDoris,
	DialectStarRocks,
	DialectMaterialize,
	DialectRisingWave,
	DialectSingleStore,
	DialectCockroachDB,
	DialectTiDB,
	DialectDruid,
	DialectSolr,
	DialectTableau,
	DialectDune,
	DialectFabric,
	DialectDrill,
	DialectDremio,
	DialectExasol,
	DialectDataFusion,
}

// Dialects returns the canonical dialect names understood by the parser and
// generator. The returned slice is independent and may be modified by the
// caller.
func Dialects() []Dialect {
	return append([]Dialect(nil), supportedDialects...)
}

// DialectCount reports the number of canonical dialects supported by this
// build. Every pair can be passed to Transpile; dialect-specific rewrites are
// applied where the Go AST has a defined equivalent and other syntax is
// preserved through the typed/raw AST boundary.
func DialectCount() int { return len(supportedDialects) }

// ParseMode controls how syntax errors are handled.
type ParseMode uint8

const (
	// Strict reports syntax errors through Parse's returned error while still
	// retaining the partial result for callers that need source context.
	Strict ParseMode = iota
	// Tolerant attempts to produce a useful partial tree and reports syntax
	// errors in ParseResult.Diagnostics.
	Tolerant
)

// ParseOptions limits parser work and selects the grammar mode. Zero values
// are useful: an empty dialect means Generic and zero limits are replaced by
// safe defaults.
type ParseOptions struct {
	Dialect       Dialect
	Mode          ParseMode
	MaxInputBytes int
	MaxTokens     int
	MaxASTNodes   int
	MaxDepth      int
}

const (
	defaultMaxInputBytes = 16 << 20
	defaultMaxTokens     = 1_000_000
	defaultMaxASTNodes   = 1_000_000
	defaultMaxDepth      = 512
)

func (o ParseOptions) normalized() (ParseOptions, error) {
	dialect, err := o.Dialect.normalized()
	if err != nil {
		return ParseOptions{}, err
	}
	o.Dialect = dialect
	if o.MaxInputBytes <= 0 {
		o.MaxInputBytes = defaultMaxInputBytes
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = defaultMaxTokens
	}
	if o.MaxASTNodes <= 0 {
		o.MaxASTNodes = defaultMaxASTNodes
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = defaultMaxDepth
	}
	return o, nil
}

// DefaultParseOptions returns safe parser defaults for a dialect.
func DefaultParseOptions(dialect Dialect) ParseOptions {
	return ParseOptions{Dialect: dialect, Mode: Strict}
}
