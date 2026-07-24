# docify-repo

`docify-repo` is a lightweight, containerized CLI tool that generates and maintains repository documentation in Continuous Integration (CI). By treating Git-tracked source files as a portable input model, it produces a living knowledge base—complete with architecture insights, dependency mapping, and Mermaid visualizations—without requiring a language-specific toolchain or executing repository code.

## Key Features

- **Language-Agnostic**: Works with repositories written in any language by analyzing Git-tracked source files.
- **LLM-Powered Documentation**: Uses deterministic LLM prompts to synthesize topic documents similar to standard operating procedures (SOPs). It covers architecture, components, interfaces, data models, workflows, dependencies, and consistency gaps.
- **Mermaid Visualizations**: Automatically generates diagrams to document the system visually.
- **CI/CD Native**: Runs seamlessly in CI through a minimal Docker image containing only the compiled Go binary, `git`, and CA certificates.
- **Automated PR Publishing**: Upon merging to the base branch, `docify-repo` processes the changes and generates a GitHub Pull Request with the updated documentation.
- **Safe & Secure**: Operates entirely within bounded contexts. The LLM never receives shell, Git, filesystem, or network access, and the tool never compiles or executes the source code.
- **Stable & Incremental**: Processes only affected components after a merge to avoid generating unnecessary documentation churn when source files have not changed.

## Getting Started

### Prerequisites

- [Go](https://golang.org/doc/install) 1.22 or higher
- Git
- Access to an OpenAI-compatible LLM

### Building the CLI

To build the `docify-repo` CLI tool locally:

```bash
go build -o bin/docify-repo ./cmd/docify-repo
```

### Running the Tool

You can run `docify-repo` locally to check or plan documentation updates:

```bash
./bin/docify-repo -help
```

*Note: For complete CI/CD integration and deployment instructions, refer to the documentation in the repository.*

## Architecture & Design

The core of `docify-repo` is a deterministic pipeline, not an open-ended autonomous agent. It predictably selects files, computes affected components, sends structured LLM requests, and renders the Markdown itself.

