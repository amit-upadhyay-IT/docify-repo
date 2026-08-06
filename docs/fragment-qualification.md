# Fragment Qualification

Fragment qualification is a release gate, not a new runtime or persisted-data
contract. It does not require a Phase 6 schema-version bump.

The configuration schema and state schema remain version `1`. Run-report schema
`5` remains the correct identity because Phase 5 added strategy, planning, runtime,
and safe-failure fields to that report. Whether the binary is distributed directly
or through a container does not change those contract identities.

## Offline Qualification

`TestFragmentQualificationPreservesKnownFactsAndDeterminism` sends one rich fixture
through both `dossier` and `fragments`. It verifies known architecture, interface,
data-model, workflow, dependency, review-gap, evidence, and diagram facts; rejects
material duplication in the component page; checks incremental no-op behavior; and
requires repeated scripted fragment runs to produce identical document bytes.

```bash
go test -count=1 ./internal/app/documentation/usecase \
  -run TestFragmentQualificationPreservesKnownFactsAndDeterminism
```

## Provider Qualification

The opt-in live test submits every embedded production fragment schema with the
minimum supported 8,192-token and 8,192-byte response budgets. It records only safe
metadata: fragment kind, structured-output mode, finish reason, attempt count, and
whether usage was present.

```bash
export DOCIFY_LIVE_LLM_BASE_URL="https://provider.example/v1"
export DOCIFY_LIVE_LLM_MODEL="exact-model-id"
export DOCIFY_LLM_API_KEY="..."
export DOCIFY_LIVE_LLM_API_MODE="chat_completions"
export DOCIFY_LIVE_LLM_STRUCTURED="json_schema"

go test -count=1 -v ./internal/app/documentation/usecase \
  -run TestLiveFragmentSchemaCompatibility
```

Run the test in strict `json_schema` mode first. If a supported provider requires
prompt fallback, repeat it with `DOCIFY_LIVE_LLM_STRUCTURED=prompt_json` and record
that limitation explicitly. At least two exact provider/model configurations must
pass before describing fragment generation as provider-neutral.

## Repository Qualification

The qualification runner builds and exercises the local binary; Docker is not
required. It creates a detached temporary worktree at the current committed
revision, preserves that revision's repository configuration, forces
`generation_strategy: fragments` through `DOCIFY_GENERATION_STRATEGY`, performs
three full runs, and checks that an incremental run after each full run is a no-op.

```bash
export DOCIFY_LLM_BASE_URL="https://provider.example/v1"
export DOCIFY_LLM_MODEL="exact-model-id"
export DOCIFY_LLM_API_KEY="..."

sh scripts/qualify-fragments.sh
```

Set `DOCIFY_QUALIFICATION_RUNS` to change the repeat count and
`DOCIFY_QUALIFICATION_OUTPUT` to change the artifact directory. The default output
is under ignored `fragment-qualification/`. Each run contains safe command output,
run reports, and a complete `repository-snapshot` after generation with only the
temporary worktree's `.git` pointer removed. Complete snapshots make custom or
ignored output paths and deletions part of the byte-stability gate. Runs after the
first also contain a repository diff against the first run, and any byte difference
fails qualification. Snapshots contain repository source and local model-produced
documents; review or delete them according to the repository's data-handling policy.

## Human Review

Compare successful dossier and fragment output using this rubric:

| Dimension | Acceptance check |
|---|---|
| Factual coverage | Critical architecture, interface, model, workflow, and dependency facts remain present |
| Evidence precision | Every factual item cites a directly supporting allowed path |
| Architecture coherence | Responsibilities and boundaries agree across sections |
| Naming | The same repository concept uses understandable, consistent terminology |
| Workflow continuity | Cross-file actors and steps remain in meaningful order |
| Duplication | Repetition does not materially reduce readability |
| Diagram usefulness | Diagrams add information and contain valid references |
| Review-gap honesty | Missing, saturated, or conflicting context is visible rather than invented |

Qualification fails on any output-token truncation, uncovered required scope,
material factual regression, invalid evidence, or unresolved provider-schema
incompatibility.

## Rollout Gate

Keep the built-in default at `dossier`. After offline, provider, repeated repository,
and human qualification pass, opt this repository into `auto` with:

```yaml
version: 1
documentation:
  generation_strategy: auto
```

Publishing and digest-pinning a container image is deferred until an actual
deployment consumes that image. If a container deployment is introduced later,
publish the qualified revision and pin the consuming workflow to the resulting
immutable digest; never invent or predeclare a digest before publication.
