package typeoracle

const FindingVersion = "v1"

// Finding is a self-contained, minimized reproduction of one oracle result.
// It records enough engine and generator identity to distinguish a Golyglot
// change from an engine-version change.
type Finding struct {
	Version       string    `json:"version"`
	Seed          int64     `json:"seed"`
	Engine        string    `json:"engine"`
	EngineVersion string    `json:"engineVersion"`
	Case          QueryCase `json:"case"`
	Result        Result    `json:"result"`
}

func NewFinding(seed int64, engine, engineVersion string, testCase QueryCase, result Result) (Finding, error) {
	reduced, err := ReduceCase(testCase)
	if err != nil {
		return Finding{}, err
	}
	result.SQL = reduced.SQL
	result.Dialect = reduced.Dialect
	result.Schema = reduced.Schema
	result.GeneratorVersion = reduced.GeneratorVersion
	result.Source = reduced.Source
	result.Features = append([]string(nil), reduced.Features...)
	return Finding{
		Version: FindingVersion, Seed: seed, Engine: engine,
		EngineVersion: engineVersion, Case: reduced, Result: result,
	}, nil
}
