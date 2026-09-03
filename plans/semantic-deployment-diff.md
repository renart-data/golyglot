# Semantic deployment diff

Status: proposed Renart architecture, with a query-level Golyglot prototype.

## Outcome

Before a deployment, Renart should explain behavioral and contract changes,
including changes propagated through SQL files whose text did not change. For
example:

```text
lineitems.total_amount  INTEGER -> DOUBLE
  -> revenue.total = SUM(total_amount)  BIGINT -> DOUBLE
```

This is an impact analysis, not a proof that two queries are equivalent.
Unknown or partial evidence must remain visible and must never be presented as
"safe".

## Architectural boundary

- Golyglot produces dialect-aware facts: canonical SQL identity, referenced
  inputs, output names/types/nullability, transformations, and lineage.
- Renart owns pipeline snapshots, rendering scenarios, graph propagation,
  deployment policy, risk levels, caching, and the review UI.
- Warehouse execution is not required for the first version. Runtime/data
  checks can be added later as a separate evidence layer.

The query-level prototype is `golyglot.DiffQuerySemantics`. It compares one
SELECT in two schema worlds and already distinguishes direct from propagated
output changes. It intentionally has no "breaking" policy.

## Formatting invariance

Semantic identity is derived from a parsed, canonically generated statement,
not from source text. Changes in whitespace, indentation, keyword/function
case, optional trailing semicolons, and ordinary comments therefore remain
visible as a source edit but produce no semantic change.

Comment removal is token-based rather than regex-based, so comment markers in
quoted strings or identifiers are preserved. Executable/directive comments
such as `/*+ ... */`, `/*! ... */`, and `--+ ...` receive a position-aware
fingerprint and remain an identity change. The later behavioral-fingerprint
phase should expose these separately as execution-hint changes.

## Compare two complete worlds

A source diff is insufficient. Build two independent semantic worlds with the
same analyzer version:

1. Baseline: the latest immutable deployed snapshot, its dependency manifest,
   and pinned producer deployments.
2. Candidate: the exact saved working tree identified by the deployment plan's
   source Merkle root and producer pins.
3. Render and infer each world independently in topological/fixpoint order.
4. Match assets by stable workspace identity, then compare their contracts and
   behavioral facts.
5. Propagate changed input contracts through unchanged downstream SQL and
   retain the shortest cause path for every reported change.

Derived semantic reports may be cached by `(source merkle, analyzer version,
render scenario digest, producer pin digest)`. Source snapshots remain the
authority.

## Report contract

```text
SemanticImpactReport
  version
  baseline { snapshot version, source merkle }
  candidate { source merkle, plan id }
  analyzer version
  scenarios[]
  complete
  assets[]
    asset id/name
    source state: identical | canonical-only | changed | added | removed
    confidence/completeness
    input changes[]
    output changes[]
    behavior changes[]
    causes[]
  summary { high, medium, low, unknown }
```

Column changes carry before/after presence, ordinal, normalized type,
nullability, confidence, transformation fingerprint, and upstream references.
A cause is a fact edge such as `input type changed`; it should not claim
causality when lineage is ambiguous.

## Initial facts and policy

Golyglot facts:

- asset/query added or removed;
- exact source identity and canonical identity;
- referenced input presence/type/nullability;
- output presence/name/order/type/nullability;
- star expansion, aggregate/function/cast transformation, and lineage;
- analysis completeness and confidence changes.

Renart policy (warning-only in the first release):

- High: removed or renamed consumed output, incompatible/narrowing type,
  analysis regressing from high-confidence to unknown, or a new parse failure.
- Medium: used type widening/change, non-null to nullable, join/filter/grouping/
  distinct change, star expansion change, or lineage source change.
- Low: unused added output, nullable to non-null, or canonical-only source edit.
- Unknown: partial template rendering, unresolved relation, ambiguous lineage,
  or unavailable cross-pipeline producer contract.

Policy must be versioned separately from facts so severity can evolve without
rewriting snapshot history.

## Rendering scenarios

Renart snapshots store authored source, while SQL rendering depends on
environment, variables, interval, and execution time.

- Phase 1 analyzes the static authoring renderer/default context and labels
  template-dependent results as partial.
- Phase 2 reuses the immutable pipeline-plan context: selected environment,
  variable digest, start/end dates, execution time, configuration digest, and
  producer pins. Baseline and candidate receive the same scenario.
- Deduplicate scenarios with identical rendered SQL hashes.
- Never silently compare a production baseline with a different candidate
  variable set.

## Renart integration

The existing immutable pipeline plan is the right consistency boundary.

- Add an optional `semantic_impact` field to deployment-purpose plans.
- Include its digest in the reviewed identity / plan ID so confirmation fails
  if source, configuration, producer pins, analyzer version, or semantic facts
  changed after review.
- Compute it in the planning service from materialized baseline and candidate
  sources; do not accept rendered SQL or semantic facts from the browser.
- Keep the current exact source-file diff below it for forensic inspection.

In `DeployPlanReview`, show one flat "Semantic impact" workbench section:

- compact counts for breaking/warning/unknown;
- affected assets, including unchanged-source downstream assets;
- an ordered column-contract table;
- expandable cause chains and evidence confidence;
- explicit "analysis incomplete" language instead of a green state.

## Delivery phases

### Phase 0: Golyglot fact prototype

- Query-level before/after schema worlds.
- Input and output contract changes.
- Exact/canonical source identity and propagated/direct origin.
- Tests for unchanged SQL plus upstream type change, cast containment,
  formatting-only change, and unknown inputs.

### Phase 1: Same-pipeline Renart report

- Materialize deployed and candidate snapshots.
- Build both canonical graphs with the existing schema-inference fixpoint.
- Compare SQL assets, output contracts, star expansions, and lineage.
- Add the read-only report to deployment plans and the review UI.
- Treat all findings as warnings; no deployment blocking.

### Phase 2: Behavioral fingerprints and policy

- Stable AST fingerprints for projection, filter, join, grouping, set
  operation, ordering/limit, and target/materialization behavior.
- Versioned severity policy and user-configurable explicit contracts.
- Cause DAG across multiple downstream assets.

### Phase 3: Cross-pipeline and rendered scenarios

- Use producer deployment pins as baseline contracts.
- Compare selected environment/variable/time scenarios.
- Cache by immutable context digests and surface scenario-specific differences.

### Phase 4: Optional runtime evidence

- Zero-row binder checks against configured warehouses.
- Bounded sampled/data-quality checks as opt-in evidence, never as a
  prerequisite for the offline semantic report.

## Acceptance tests

- Same SQL and schema: no changes.
- Formatting-only edit (including comments/case/semicolon): canonical-only,
  no semantic change.
- Comment markers inside a string literal: literal preserved, no false edit.
- Optimizer/directive comment changed or moved: canonical identity changed.
- Same SQL, input INTEGER to DOUBLE: propagated SUM output change.
- Explicit cast: input change visible, output contract stable.
- Upstream added column through `SELECT *`: added output.
- Left join nullability change: nullable output warning.
- Filter/join/grouping change with stable output schema: behavior warning.
- Unknown input type: incomplete, never safe.
- Multi-hop A -> B -> C: downstream cause chain names the original change.
- Candidate source changes after review: plan confirmation conflict.
- Cross-pipeline producer pin changes: report invalidated and regenerated.

## Operational guardrails

- Bound assets, AST size, inference rounds, and wall-clock time per report.
- Fix recursion/cycle bugs in Golyglot and retain crash regressions before
  enabling whole-pipeline analysis in a long-lived server process.
- Cache only successful versioned facts; never cache a timeout as "no change".
- Emit timings and incomplete-reason counts so performance and confidence are
  observable.

## Related approaches

- [SQLMesh plans](https://sqlmesh.readthedocs.io/en/stable/concepts/plans/)
  distinguish directly and indirectly modified models.
- [dbt state selection](https://docs.getdbt.com/reference/node-selection/methods)
  separates body/config/contract changes.
- [SQLGlot AST diff](https://sqlglot.com/sqlglot/diff.html) is a useful model
  for structural edit facts, but Renart still needs two schema worlds and
  pipeline propagation.
