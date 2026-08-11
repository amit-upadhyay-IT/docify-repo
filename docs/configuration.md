# Configuration

`docify-repo` loads configuration in this order: built-in defaults,
`.docify.yml`, supported `DOCIFY_*` environment variables, and CLI flags.
Unknown YAML fields and configuration versions other than `1` are rejected.

Source roles can be overridden with an ordered list. The first matching pattern
wins:

```yaml
version: 1

source:
  role_overrides:
    - pattern: "features/**/*.feature"
      role: contract
```

Supported override roles are `production_source`, `contract`, `database`,
`runtime_configuration`, `dependency_manifest`, `test`, `generated_code`,
`fixture`, `prose`, and `unknown_source`. Secret filename denial, binary
detection, file-size limits, and tool-owned output exclusion cannot be
overridden.

LLM output is bounded independently from request input:

```yaml
llm:
  max_output_tokens: 8192
  max_response_bytes: 65536
```

`max_response_bytes` limits extracted model content and is not derived from
`components.max_request_bytes`.

The bounded fragment profile `v1` supports providers with at least 8,192 output
tokens and an 8,192-byte response-content ceiling. Its conservative feasibility
proof budgets one output token for every accepted response byte and separately
proves that the largest compact canonical fragment fits that ceiling. Fragment
bodies above 8,192 bytes are rejected even when the adapter's global
response ceiling is higher. Exact primary request sizes and a worst-case escaped
repair envelope are checked independently against `components.max_request_bytes`.

Adaptive generation and forced fragment generation remain opt-in until the exact
provider/model and generated-document quality have been qualified:

```yaml
documentation:
  generation_strategy: auto
llm:
  fragment_call_limit_per_component: 80
  fragment_split_depth: 3
```

The built-in `generation_strategy` remains `dossier`. `auto` uses one feasible
full-dossier request as its fast path, selects fragments before any paid call when
input batching is required, and switches a truncated fast path to fragments.
`fragments` forces fragment mode for every component. Fragment map, reducer,
repair, fallback, and source-split calls share the per-component logical-call
ceiling and the global `llm.concurrency` limit. `fragment_split_depth` bounds
runtime narrowing from 0 through 16 levels; valid saturation at that limit is
retained with an explicit review gap, while truncation still fails the component.

Plan output reports primary and typical logical calls, independent maximum repair,
fallback, and source-split bounds, and the strict per-run logical-call ceiling. The
maximum HTTP-attempt estimate also accounts for `llm.retries` and both structured
output modes when `structured_output_mode` is `auto`. Fragment request and
worst-case repair bytes are grouped by fragment kind; contingent `auto` fallback
bytes are reported separately from planned primary bytes.

Run-report schema version 5 includes the same generation strategy and planned call
bounds as human and JSON command output, plus safe runtime fragment, fallback,
split, saturation, reducer, repair, and transport-attempt counts. Failed generation
and publishing reports never include source, prompts, schemas, or model prose.
Phase 6 qualification and rollout do not change this schema. Configuration and
state also remain at schema version 1 because no persisted fields are added.

An existing state path that cannot be decoded as Docify state is never overwritten
by a normal run. `--full` explicitly authorizes replacing that state path and only
the deterministic generated files reproduced by the candidate; unrelated files
remain protected.

The fragment-capable planner, prompt, input-hash, merge, and renderer identities
participate in existing state compatibility and configuration hashes. Upgrading
from an older identity triggers one full regeneration while valid generated paths
and content hashes continue to prove ownership; no fragments or model output are
persisted in state.

The built-in strategy must remain `dossier` during qualification. Use the local
binary workflow in [Fragment Qualification](fragment-qualification.md) to force
`fragments`; opt a repository into `auto` only after its provider and human quality
gates pass.

The source controls available in configuration version 1 are:

```yaml
source:
  include: ["**/*"]
  exclude: ["vendor/**"]
  max_file_bytes: 1048576
  tests:
    include_as_context: true
    trigger_on_change: false
  generated:
    include: false
    trigger_on_change: false
  fixtures:
    include_as_context: false
    trigger_on_change: false
```

`DOCIFY_GENERATION_STRATEGY` may temporarily override the configured strategy for
qualification or troubleshooting. Generation hashes use the resulting strategy,
so this override cannot incorrectly reuse state from another strategy.

Secrets are accepted only through `DOCIFY_LLM_API_KEY` and
`DOCIFY_GITHUB_TOKEN`. Configuration stores only whether these variables are
present; token values are not copied into application command models.
