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

## Serve multiple models

Stop the previous server, then start both example profiles:

```sh
make build
./bin/polyjuice-stream serve --config examples/configs/multi-model.yaml
```

From another terminal, discover the names and request each one:

```sh
curl -fsS http://127.0.0.1:9900/v1/models

curl -fsSN http://127.0.0.1:9900/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"random-model","messages":[{"role":"user","content":"Hello"}],"stream":true}'

curl -fsS http://127.0.0.1:9900/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"replay-model","messages":[{"role":"user","content":"Hello"}],"stream":false}'
```

Expect discovery to list `random-model` and `replay-model`. The random profile sends ten content chunks; the first replay request returns `Hello LLM`. Random requests do not advance the replay cursor. The next replay requests return `Next text`, then `Hello LLM`.

Unknown models return a JSON error with HTTP 404:

```sh
curl -sS -i http://127.0.0.1:9900/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"missing-model","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

Model names must match the YAML keys. The single-model request files use `mock-model`, which is not present in this example configuration. Dataset paths resolve relative to the YAML file, so the configuration works when launched from another working directory.

Use `--listen 127.0.0.1:9901` or `--write-timeout 5s` alongside `--config` to override server settings. Model flags such as `--model`, `--dataset` and `--interval` cannot be combined with `--config`; put those settings in each profile. Configuration and all datasets are validated before listening; an invalid model prevents the whole service from starting.

## Observe slow and fast models together

Restart with the [isolation example](../examples/configs/model-isolation.yaml):

```sh
./bin/polyjuice-stream serve --config examples/configs/model-isolation.yaml
```

`slow-model` waits five seconds between content chunks; `fast-model` has no simulated delay. In another terminal, this block starts a slow stream in the background, requests the fast model, then cancels and cleans up the slow client:

```sh
(
  curl -fsSN --max-time 20 http://127.0.0.1:9900/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"slow-model","messages":[{"role":"user","content":"Slow"}],"stream":true}' >&2 &
  slow_request_pid=$!
  trap 'kill "$slow_request_pid" 2>/dev/null || true; wait "$slow_request_pid" 2>/dev/null || true' EXIT
  sleep 1
  curl -fsS --max-time 5 -w '\nFast request took %{time_total}s\n' \
    http://127.0.0.1:9900/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"fast-model","messages":[{"role":"user","content":"Fast"}],"stream":false}'
)
```

On a fresh server, the slow stream first emits `"Slow "`; the fast response returns `"Fast response one."` while that stream is still waiting. Its next request returns `"Fast response two."`. Cancelling the slow request consumes only the slow model's claimed record.

This demonstrates independent request progress, not a latency or capacity guarantee. Models share CPU, memory and network resources. The automated tests also block a writer directly to check that network sends do not hold source locks.

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

These include cyclic replay, Unicode reconstruction, cancellation after selection, startup failures, independent model cursors and random sequences, and concurrent slow/fast requests. `./bin/polyjuice-stream serve --help` lists available options. Container packaging and real gateway integrations remain planned.
