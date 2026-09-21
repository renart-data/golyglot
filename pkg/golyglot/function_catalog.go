package golyglot

import (
	"fmt"
	"strings"
)

// FunctionCatalogSpec declares warehouse extensions and UDF signatures without
// executing SQL. By default it extends builtins; Strict rejects undeclared
// functions instead. A catalog is local to one validation/analysis call.
type FunctionCatalogSpec struct {
	Functions     []FunctionSpec `json:"functions"`
	CaseSensitive bool           `json:"caseSensitive,omitempty"`
	Strict        bool           `json:"strict,omitempty"`
}

type FunctionKind string

const (
	FunctionScalar    FunctionKind = "scalar"
	FunctionAggregate FunctionKind = "aggregate"
	FunctionWindow    FunctionKind = "window"
)

type FunctionSpec struct {
	Name          string              `json:"name"`
	Overloads     []FunctionSignature `json:"overloads"`
	Kind          FunctionKind        `json:"kind,omitempty"`
	CaseSensitive *bool               `json:"caseSensitive,omitempty"`
}

// Parameters and ReturnType are SQL type names. ANY accepts an arbitrary
// argument type. Variadic repeats the last parameter one or more times.
// An omitted Nullable conservatively leaves return nullability unknown.
type FunctionSignature struct {
	Parameters []string `json:"parameters"`
	ReturnType string   `json:"returnType"`
	Variadic   bool     `json:"variadic,omitempty"`
	Nullable   *bool    `json:"nullable,omitempty"`
}

type compiledFunctionCatalog struct {
	functions []compiledFunction
	strict    bool
}
type compiledFunction struct {
	name      string
	sensitive bool
	kind      FunctionKind
	overloads []compiledSignature
}
type compiledSignature struct {
	parameters  []DataType
	result      DataType
	variadic    bool
	nullability string
}

func compileFunctionCatalog(spec *FunctionCatalogSpec, dialect Dialect) (*compiledFunctionCatalog, error) {
	if spec == nil {
		return nil, nil
	}
	catalog := &compiledFunctionCatalog{strict: spec.Strict}
	for _, definition := range spec.Functions {
		if strings.TrimSpace(definition.Name) == "" || definition.Name != strings.TrimSpace(definition.Name) || len(definition.Overloads) == 0 {
			return nil, fmt.Errorf("function catalog requires a nonempty name and at least one overload")
		}
		kind := definition.Kind
		if kind == "" {
			kind = FunctionScalar
		}
		if kind != FunctionScalar && kind != FunctionAggregate && kind != FunctionWindow {
			return nil, fmt.Errorf("invalid function kind %q", kind)
		}
		fn := compiledFunction{name: definition.Name, sensitive: spec.CaseSensitive, kind: kind}
		if definition.CaseSensitive != nil {
			fn.sensitive = *definition.CaseSensitive
		}
		for _, previous := range catalog.functions {
			if previous.name == fn.name || (!(previous.sensitive && fn.sensitive) && strings.EqualFold(previous.name, fn.name)) {
				return nil, fmt.Errorf("overlapping function catalog names %q and %q", previous.name, fn.name)
			}
		}
		for _, signature := range definition.Overloads {
			if signature.Variadic && len(signature.Parameters) == 0 {
				return nil, fmt.Errorf("variadic function %q needs a repeated parameter", fn.name)
			}
			if strings.TrimSpace(signature.ReturnType) == "" {
				return nil, fmt.Errorf("function %q needs a return type", fn.name)
			}
			result, err := ParseDataType(signature.ReturnType, dialect)
			if err != nil {
				return nil, fmt.Errorf("function %q return type: %w", fn.name, err)
			}
			overload := compiledSignature{result: result, variadic: signature.Variadic, nullability: nullabilityUnknown}
			if signature.Nullable != nil {
				overload.nullability = nullabilityNonNull
				if *signature.Nullable {
					overload.nullability = nullabilityNullable
				}
			}
			for _, parameter := range signature.Parameters {
				if strings.EqualFold(parameter, "ANY") {
					overload.parameters = append(overload.parameters, DataType{Kind: DataTypeUnknown})
					continue
				}
				if strings.TrimSpace(parameter) == "" {
					return nil, fmt.Errorf("function %q has an empty parameter type", fn.name)
				}
				parsed, err := ParseDataType(parameter, dialect)
				if err != nil {
					return nil, fmt.Errorf("function %q parameter type: %w", fn.name, err)
				}
				overload.parameters = append(overload.parameters, parsed)
			}
			fn.overloads = append(fn.overloads, overload)
		}
		catalog.functions = append(catalog.functions, fn)
	}
	return catalog, nil
}

func (catalog *compiledFunctionCatalog) lookup(name string) *compiledFunction {
	if catalog == nil {
		return nil
	}
	for i := range catalog.functions {
		fn := &catalog.functions[i]
		if fn.name == name || (!fn.sensitive && strings.EqualFold(fn.name, name)) {
			return fn
		}
	}
	return nil
}

func (catalog *compiledFunctionCatalog) infer(fn *FunctionCallExpr, args []inferredExpression, issues *[]semanticIssue) (inferredExpression, bool) {
	unknown := inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown}
	if catalog == nil || fn.ArrayLiteral {
		return unknown, false
	}
	name := identifiersText(fn.Name)
	definition := catalog.lookup(name)
	report := func(code, message string) {
		if issues != nil {
			*issues = append(*issues, semanticIssue{code: code, message: message, span: fn.SourceSpan()})
		}
	}
	if definition == nil {
		if catalog.strict {
			report("E220", fmt.Sprintf("function %q is not declared in the supplied catalog", name))
			return unknown, true
		}
		return unknown, false
	}
	if fn.RawArgs != "" {
		return unknown, true
	}
	var matches []compiledSignature
	bestCost := int(^uint(0) >> 1)
	arity := false
	for _, signature := range definition.overloads {
		if len(args) != len(signature.parameters) && !(signature.variadic && len(args) >= len(signature.parameters)) {
			continue
		}
		arity = true
		compatible := true
		cost := 0
		for i, arg := range args {
			index := i
			if index >= len(signature.parameters) {
				index = len(signature.parameters) - 1
			}
			compatible = compatible && functionParameterAccepts(signature.parameters[index], arg.dataType)
			cost += functionParameterCost(signature.parameters[index], arg.dataType)
		}
		if compatible {
			if cost < bestCost {
				matches = nil
				bestCost = cost
			}
			if cost == bestCost {
				matches = append(matches, signature)
			}
		}
	}
	if !arity {
		report("E221", fmt.Sprintf("function %q has no overload accepting %d arguments", name, len(args)))
		return unknown, true
	}
	if len(matches) == 0 {
		report("E222", fmt.Sprintf("argument types do not match any overload of function %q", name))
		return unknown, true
	}
	result := inferredExpression{dataType: matches[0].result, nullability: matches[0].nullability}
	for _, match := range matches[1:] {
		if !semanticDataTypesEqual(result.dataType, match.result) {
			result.dataType = DataType{Kind: DataTypeUnknown}
		}
		result.nullability = combineNullability(result.nullability, match.nullability)
	}
	return result, true
}

// Prefer exact signatures to widening conversions and ANY. Unknown arguments
// cannot break a tie: a catalog must not invent a type from overload order.
func functionParameterCost(want, actual DataType) int {
	if !actual.Known() || semanticDataTypesEqual(want, actual) {
		return 0
	}
	if !want.Known() {
		return 100
	}
	if want.Element != nil && actual.Element != nil {
		return functionParameterCost(*want.Element, *actual.Element)
	}
	if want.Kind == actual.Kind {
		return 1
	}
	return 2
}

func functionParameterAccepts(want, actual DataType) bool {
	if !want.Known() || !actual.Known() {
		return true
	}
	if semanticDataTypesEqual(want, actual) {
		return true
	}
	if (want.Kind == DataTypeArray || want.Kind == DataTypeList) && (actual.Kind == DataTypeArray || actual.Kind == DataTypeList) && want.Element != nil && actual.Element != nil {
		return functionParameterAccepts(*want.Element, *actual.Element)
	}
	if want.Kind == actual.Kind && want.Kind != DataTypeCustom && want.Kind != DataTypeStruct {
		return true
	}
	if isSemanticNumeric(want) && isSemanticNumeric(actual) {
		order := map[DataTypeKind]int{DataTypeTinyInt: 1, DataTypeSmallInt: 2, DataTypeInteger: 3, DataTypeBigInt: 4, DataTypeHugeInt: 5, DataTypeDecimal: 6, DataTypeFloat: 7, DataTypeDouble: 8}
		return order[actual.Kind] <= order[want.Kind]
	}
	return false
}

func (c *validationCollector) aggregateFunction(fn *FunctionCallExpr) bool {
	if definition := c.catalog.lookup(identifiersText(fn.Name)); definition != nil {
		return definition.kind == FunctionAggregate
	}
	return isAggregateFunctionName(identifiersText(fn.Name), c.dialect)
}
