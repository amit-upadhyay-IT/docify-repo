package config

func defaults() Config {
	return Config{
		Version:          CurrentVersion,
		DocsDir:          "docs/generated",
		StatePath:        ".docify/state.json",
		WorkingDirectory: ".",
		Source: SourceConfig{
			Include: []string{"**/*"},
			Exclude: []string{
				"vendor/**",
				"node_modules/**",
				"dist/**",
				"build/**",
				"**/*.min.js",
				"**/*.min.css",
				"**/*.map",
				"**/*.lock",
			},
			MaxFileBytes: 1 << 20,
			Tests: SourceBehavior{
				IncludeAsContext: true,
				TriggerOnChange:  false,
			},
			Generated: SourceBehavior{
				IncludeAsContext: false,
				TriggerOnChange:  false,
			},
			Fixtures: SourceBehavior{
				IncludeAsContext: false,
				TriggerOnChange:  false,
			},
		},
		Components: ComponentsConfig{
			Strategy:           "inferred",
			MaxContextBytes:    120_000,
			MaxBatchBytes:      80_000,
			MaxSupportingBytes: 20_000,
			MaxManifestBytes:   20_000,
			MaxDiffBytes:       40_000,
			MaxRequestBytes:    200_000,
		},
		Documentation: DocumentationConfig{
			Profile:  "codebase-summary",
			Audience: "mixed",
			Mermaid:  true,
		},
		LLM: LLMConfig{
			Provider:             "openai-compatible",
			APIMode:              "chat_completions",
			Temperature:          0,
			MaxOutputTokens:      8_192,
			StructuredOutputMode: "auto",
			Timeout:              "60s",
			Retries:              2,
			Concurrency:          2,
		},
		Publishing: PublishingConfig{Provider: "worktree"},
	}
}
