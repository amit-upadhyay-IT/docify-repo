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

Secrets are accepted only through `DOCIFY_LLM_API_KEY` and
`DOCIFY_GITHUB_TOKEN`. Configuration stores only whether these variables are
present; token values are not copied into application command models.
