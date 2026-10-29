# Repository layout

Polyjuice Stream uses `polyjuice-stream` for its executable and release name. Stages 1 and 2 contain a runnable Go server with random output or JSONL replay, tests, documentation and synthetic examples. Multi-model configuration and container packaging are planned.

```text
llm-engine-generator/
├── README.md
├── AGENTS.md
├── .editorconfig
├── .gitignore
├── go.mod
├── Makefile
├── .github/workflows/ci.yml
├── docs/
│   ├── design-v0.1.md
│   ├── http-contract.md
│   ├── manual-testing.md
│   └── repository-layout.md
├── src/
│   ├── cmd/polyjuice-stream/  # Executable entry point
│   ├── config/           # Flags, defaults and validation; YAML planned
│   ├── source/           # Random text and validated cyclic JSONL replay
│   ├── generation/       # Shared response plans, chunking and timing
│   └── httpapi/          # Request validation, SSE and JSON responses
├── tests/
│   ├── config/
│   ├── source/
│   ├── generation/
│   ├── httpapi/
│   ├── integration/
│   └── fixtures/
├── examples/
│   ├── configs/           # Mock model and LiteLLM configurations
│   ├── datasets/          # Synthetic result, custom-field and Unicode records
│   └── requests/          # Streaming and complete JSON request examples
├── assets/images/        # Documentation diagrams and screenshots
└── scripts/              # Development helpers when needed
```

The project keeps source in `src/`, mirrored tests in `tests/`, and static assets in `assets/`. Go packages are the directories under `src/`; a second `pkg/` layer would duplicate that grouping. These packages are implementation details, not a promised stable library API.

The Go module, Makefile and CI live at the repository root. Use `make setup`, `make dev`, `make build`, `make lint`, `make test` and `make test-race`. The [manual guide](manual-testing.md) gives copyable run, request and debugging commands. `make build` writes the executable to ignored `bin/polyjuice-stream`. Remaining placeholder directories contain `.gitkeep` files until they are needed.

Use `examples/datasets/` for small, synthetic, shareable records. Local user datasets may live in ignored `data/`. Only PNG diagrams from `assets/images/` are published. Editable Excalidraw sources, SVG exports, conversation archives and conversation reference images are kept locally and excluded from Git. The release will include an executable and a container image; the future Dockerfile will live at the root.

Name check on 3 October 2026: no matching public repository name or published package was found for `polyjuice-stream` in GitHub, npm or PyPI. This is a search result, not a name reservation.

## Reference repositories

Reviewed on 3 October 2026:

| Repository | Relevant existing directories | What we adopt |
|---|---|---|
| [llm-d-inference-sim](https://github.com/llm-d/llm-d-inference-sim) | `cmd/`, `pkg/`, `docs/`, `scripts/`, plus deployment and test directories | Separate the executable, implementation packages, documentation and helpers. |
| [Microsoft Vidur](https://github.com/microsoft/vidur) | `vidur/`, `docs/`, `assets/`, `data/` | Separate code, documentation, visual assets and data. |

llm-d also has `deploy/`, `helm/`, `manifests/`, `hooks/`, `test/images/` and `zmq-listener/`. These support its wider system and are not required for this first HTTP server. Vidur's research data and simulation components are references for organisation, not dependencies of this project.

Go's [module layout guide](https://go.dev/doc/modules/layout#server-project) commonly uses root-level `cmd/` and `internal/`. Our `src/` grouping is a project convention, not a requirement imposed by Go.
