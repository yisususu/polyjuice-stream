# Run and debug locally

Run these commands from the repository root. Stop a running server with `Ctrl-C` before starting another on the same port. If port 9900 is busy, add `--listen 127.0.0.1:9901` and use that port in the requests.

## Build and run random output

```sh
make build
./bin/polyjuice-stream serve --chunks 3 --chunk-chars 3 \
  --first-delay 100ms --interval 30ms --seed 42
```

In another terminal:

```sh
curl -fsS http://127.0.0.1:9900/health
curl -fsS http://127.0.0.1:9900/v1/models
curl -fsSN http://127.0.0.1:9900/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d @examples/requests/random-stream.json
```

Expect three content events, a finish event and `[DONE]`. With negligible processing and network delay, content starts around 100, 130 and 160ms. `curl -N` disables curl's output buffering.

## Replay JSONL

Stop the random server, then start:

```sh
./bin/polyjuice-stream serve --dataset examples/datasets/responses.jsonl \
  --chunk-chars 3 --first-delay 100ms --interval 30ms
```

The default field is `result`; the default model name remains `mock-model`. From another terminal, make three complete requests:

```sh
for request in 1 2 3; do
  curl -fsS http://127.0.0.1:9900/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d @examples/requests/random-complete.json |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["choices"][0]["message"]["content"])'
done
```

Expect `Hello LLM`, `Next text`, `Hello LLM`. Prompts do not affect selection. Streaming and complete requests share the cursor; restart the server to return to record 1. The existing request files work with either source because their model name is `mock-model`.

To see the SSE wire format:

```sh
curl -fsSN http://127.0.0.1:9900/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d @examples/requests/random-stream.json
```

On a fresh server, the content deltas are `"Hel"`, `"lo "` and `"LLM"`.

## Select another field or check Unicode

Restart with either command:

```sh
# Read the exact top-level key named output
./bin/polyjuice-stream serve --dataset examples/datasets/custom-output.jsonl \
  --text-field output

# Split by Unicode code point, preserving Chinese and emoji when joined
./bin/polyjuice-stream serve --dataset examples/datasets/unicode.jsonl \
  --chunk-chars 1 --first-delay 0s --interval 30ms
```

Use the same curl requests above. For Unicode, the complete records are `你好，世界 🌍` and `第二条回复：你好 👋🏽`. A multi-code-point emoji may span deltas; concatenating them reproduces the original string exactly.

The file is loaded once at startup. Editing it does not change a running service; restart to load the new contents. `--chunks` and `--seed` are for random output and cannot be combined with `--dataset`. Use the request's `max_tokens` or `max_completion_tokens` to cap replay chunks.

## Cancel a claimed record

Restart with long gaps so the first response stays open:

```sh
./bin/polyjuice-stream serve --dataset examples/datasets/responses.jsonl \
  --chunk-chars 3 --first-delay 0s --interval 1s
```

In another terminal:

```sh
# Stop after the first content has arrived; curl's timeout exit is expected.
curl -sSN --max-time 0.5 http://127.0.0.1:9900/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d @examples/requests/random-stream.json

# The next request claims record 2, capped at one chunk: "Nex".
curl -fsS http://127.0.0.1:9900/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock-model","messages":[{"role":"user","content":"Ignored"}],"max_tokens":1}'
```

Cancellation consumes a record once claimed. Requests rejected during validation do not advance the cursor.

## Catch bad data before serving

Stop the server, then create a local file with a missing field on line 2:

```sh
mkdir -p data
printf '%s\n' '{"result":"Valid first record"}' '{"output":"Missing result"}' > data/broken.jsonl
./bin/polyjuice-stream serve --dataset data/broken.jsonl
```

Expect a nonzero exit and an error containing `data/broken.jsonl:2`, with no `Listening on` message. Malformed JSON, invalid Unicode, empty text and size-limit failures also stop startup. See the [limits and field rules](http-contract.md#jsonl-replay).

## Run the checks

```sh
make lint
make test
make test-race
```

To rerun source, HTTP and executable checks with test names visible:

```sh
go test -v ./tests/source ./tests/httpapi ./tests/integration
```

These include cyclic replay, Unicode reconstruction, cancellation after selection and startup failures. `./bin/polyjuice-stream serve --help` lists available options. Multi-model YAML configuration and container packaging remain planned.
