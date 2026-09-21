package golyglot

import (
	"embed"
	"encoding/json"
	"slices"
	"sync"
)

// FunctionTable is a table-producing builtin. It is editor metadata, not a
// supported FunctionCatalogSpec UDF kind (UDF inference remains scalar-valued).
const FunctionTable FunctionKind = "table"

// BuiltinFunctionCatalog combines a versioned engine inventory with separately
// verified grammar-level expression forms. It is not an exhaustive SQL grammar
// or a function type checker.
type BuiltinFunctionCatalog struct {
	Dialect       Dialect           `json:"dialect"`
	EngineVersion string            `json:"engineVersion"`
	Source        string            `json:"source"`
	Functions     []BuiltinFunction `json:"functions"`
}

type BuiltinFunction struct {
	Name          string             `json:"name"`
	Kind          FunctionKind       `json:"kind"`
	Description   string             `json:"description,omitempty"`
	Signatures    []BuiltinSignature `json:"signatures,omitempty"`
	CaseSensitive bool               `json:"caseSensitive,omitempty"`
	// Source overrides the inventory provenance for grammar-level forms.
	Source string `json:"source,omitempty"`
}

// BuiltinSignature preserves catalog-declared types, including polymorphic
// placeholders. Grammar-level forms use ANY for arbitrary expressions rather
// than promising an engine overload. Empty ReturnType means unknown, notably for file-dependent
// table functions and ClickHouse's prose-only inventory. Never substitute this
// metadata for AnalyzeQuery's argument-dependent inference.
type BuiltinSignature struct {
	// Parameters excludes the repeated tail represented by VariadicType.
	Parameters   []BuiltinParameter `json:"parameters,omitempty"`
	ReturnType   string             `json:"returnType,omitempty"`
	VariadicType string             `json:"variadicType,omitempty"`
	Syntax       string             `json:"syntax,omitempty"`
}

type BuiltinParameter struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Named    bool   `json:"named,omitempty"`
}

//go:embed function_catalogs/*.json
var builtinFunctionFiles embed.FS

var builtinFunctionCatalogs = map[Dialect]func() BuiltinFunctionCatalog{
	DialectDuckDB:     loadBuiltinFunctionCatalog("duckdb"),
	DialectPostgreSQL: loadBuiltinFunctionCatalog("postgresql"),
	DialectClickHouse: loadBuiltinFunctionCatalog("clickhouse"),
}

func loadBuiltinFunctionCatalog(name string) func() BuiltinFunctionCatalog {
	return sync.OnceValue(func() BuiltinFunctionCatalog {
		data, err := builtinFunctionFiles.ReadFile("function_catalogs/" + name + ".json")
		if err != nil {
			panic(err)
		} // checked-in build artifact
		var catalog BuiltinFunctionCatalog
		if err := json.Unmarshal(data, &catalog); err != nil {
			panic(err)
		}
		return withBuiltinSpecialForms(catalog)
	})
}

// BuiltinFunctionCatalogForDialect returns an independent copy of the shipped
// builtin inventory. Unsupported dialects return false, never a compatible
// vendor's inventory. No connection, extension loading, or SQL execution occurs.
func BuiltinFunctionCatalogForDialect(dialect Dialect) (BuiltinFunctionCatalog, bool) {
	load, ok := builtinFunctionCatalogs[dialect]
	if !ok {
		return BuiltinFunctionCatalog{}, false
	}
	catalog := load()
	catalog.Functions = slices.Clone(catalog.Functions)
	for i := range catalog.Functions {
		catalog.Functions[i].Signatures = slices.Clone(catalog.Functions[i].Signatures)
		for j := range catalog.Functions[i].Signatures {
			catalog.Functions[i].Signatures[j].Parameters = slices.Clone(catalog.Functions[i].Signatures[j].Parameters)
		}
	}
	return catalog, true
}
