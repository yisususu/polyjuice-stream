# Polyjuice Stream

![Polyjuice Stream: a hand-drawn potion bottle turning text into a stream.](assets/images/readme-logo.png)

**One command. Mock LLM responses. No accelerators.**

Turn random text or recorded LLM outputs into a model service for testing clients, gateways, routers and dispatchers—without running a model or requiring expensive accelerators.

> **Available now:** random output or JSONL replay, multiple model profiles, configurable timing, SSE and complete JSON responses. Container packaging is next.

## Why Polyjuice?

In Harry Potter, the [Polyjuice Potion](https://www.harrypotter.com/fact-file/plants-and-potions/polyjuice-potion) changes a person's appearance into another person's. Polyjuice Stream gives simulated responses the outward form of real LLM output, such as vLLM streams, without running a model or requiring expensive accelerators.

[Design](docs/design-v0.1.md) · [HTTP contract](docs/http-contract.md) · [Repository layout](docs/repository-layout.md) · [Run and debug](docs/manual-testing.md)

## Designed to be easy

Build from source with Go 1.25 or later, then start the server:

```sh
make build
./bin/polyjuice-stream serve
```

No configuration is needed. Point your client at `http://127.0.0.1:9900/v1`, using model name `mock-model`.

![Start the executable, connect your client or gateway, and receive mock responses.](assets/images/readme-quickstart.png)

In another terminal:

```sh
curl -N http://127.0.0.1:9900/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d @examples/requests/random-stream.json
```

Set `stream` to `false` for one complete response after the same simulated generation time. To control the output length and pace:

```sh
./bin/polyjuice-stream serve --chunks 1000 --first-delay 30ms --interval 30ms
```

Defaults: 100 chunks, 4 characters per chunk, a 100ms first delay and a 30ms interval. Use `--model` to change the model name, `--seed` for repeatable text, or `--listen` to change the address. Run directly from source with `go run ./src/cmd/polyjuice-stream serve`.

To replay recorded output instead, restart with:

```sh
./bin/polyjuice-stream serve --dataset examples/datasets/responses.jsonl
```

Records cycle in order, ignoring prompts. Each line must contain a non-empty `result` string; use `--text-field output` for another top-level key. The entire file is checked before the server starts. Both response modes use the same replay cursor. See [manual checks](docs/manual-testing.md) for Unicode, cancellation and bad-data examples.

To serve multiple models on the same endpoint, restart with:

```sh
./bin/polyjuice-stream serve --config examples/configs/multi-model.yaml
```

Set the request's `model` to `random-model` or `replay-model`. Each model has its own content source, cursor or random sequence, and timing. `GET /v1/models` lists the configured names. See [multi-model commands](docs/manual-testing.md#serve-multiple-models) to try both.

## v0.1 goals

![Random or recorded text, multiple model names, streaming or complete responses, and no accelerator required.](assets/images/readme-goals.png)

- **Available:** generate random text or replay JSONL; give each model its own source and pace; return SSE or complete JSON.
- **Planned packaging:** a downloadable executable and a container image.

Output units are simulated text chunks, not real model tokens. See the [visual design](docs/design-v0.1.md) for timing and API scope.
