# v0.1 contract appendix

Stage 1 implements the HTTP API below for one random-output model. JSONL and multi-model rules are labelled as planned. Start with the [visual design](design-v0.1.md).

## Endpoints and fields

| Item | v0.1 behaviour |
|---|---|
| `POST /v1/chat/completions` | One assistant choice; text messages only. |
| `GET /v1/models` | Advertise the configured model name (`mock-model` by default). |
| `GET /health` | Ready after configuration validation. |
| `model`, `messages` | Require a known model and a non-empty array of messages with string content. Prompt contents do not choose the response. |
| `stream` | Defaults to false; both modes share content planning and timing. |
| `max_tokens`, `max_completion_tokens` | Positive simulated-chunk limits. Equal values are accepted together; conflicting values return 400. |
| `temperature`, `top_p`, penalties | Accepted and ignored; these do not change the generated text. |
| `stream_options.include_usage` | Accepted for Sidecar compatibility, but v0.1 omits usage accounting. Other stream options are unsupported. |
| `n` | Omitted or 1 only. |
| Tools, multimodal input, structured output, `stop` | Unsupported: return a clear 400 error. |
| Unrelated extension fields, e.g. `user` | Accept and ignore. Core fields and their types remain validated; explicitly unsupported features and stream options still return 400. |

Unknown models return 404. Malformed or trailing JSON returns 400; bodies over 1 MiB return 413; unsupported HTTP methods return 405. Request headers and body must arrive within the server's 10s read timeout; this is separate from the per-write timeout and does not cap simulated generation time. Validation errors return JSON with `error.message`, `error.type`, `error.param` where applicable and `error.code`. Validate before starting SSE; after headers are sent, transport failure terminates the stream.

Use `finish_reason: length` only if the client limit truncates planned content; otherwise use `stop`. Do not represent chunk counts as tokenizer-accurate usage.

## Response examples

These are illustrative wire-format examples for three chunks, `"Hel"`, `"lo "` and `"LLM"`; actual random text, IDs and timestamps vary. Use `--chunks 3 --chunk-chars 3` for this shape. Joining the deltas gives the complete response text. A seeded restart reproduces assigned text; successive requests still select new output. LiteLLM may normalise its downstream envelope. Real vLLM streams incrementally decoded model output; it does not split at a fixed character count, and a text delta can combine multiple tokens.

### SSE (`Content-Type: text/event-stream`)

```text
data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":1790985600,"model":"mock-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}

data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":1790985600,"model":"mock-model","choices":[{"index":0,"delta":{"content":"lo "},"finish_reason":null}]}

data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":1790985600,"model":"mock-model","choices":[{"index":0,"delta":{"content":"LLM"},"finish_reason":null}]}

data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":1790985600,"model":"mock-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

```

The first role and content share an event. Flush each content event. Metadata and termination events do not count toward output limits.

### Complete JSON (`Content-Type: application/json`)

```json
{
  "id": "chatcmpl-example",
  "object": "chat.completion",
  "created": 1790985600,
  "model": "mock-model",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "Hello LLM"},
    "finish_reason": "stop"
  }]
}
```

## Content and timing

- Random output uses printable ASCII. `--chunks` sets the output length; `--chunk-chars` sets the size of each chunk. Limits: 100,000 chunks, 4,096 characters per chunk and 4 MiB per planned response.
- `--seed` reproduces the text sequence in server request-assignment order; omit it for a fresh seed at startup. Concurrent arrival order can change which client receives each text. Prompts do not choose or change content.
- After planning, wait `--first-delay` before sending the first role and content event together. After each successful write and flush, wait at least `--interval` before starting the next chunk. Never catch up with a burst or drop content.
- Non-streaming waits for the same simulated generation time, then returns the joined text. For N chunks, the nominal delay is `first_delay + (N - 1) × interval`. Streaming writes can extend the actual duration; flush completion is not proof of client receipt.
- Stop on cancellation or a write/flush error. Each write has a bounded deadline (`--write-timeout`, default 10s), so a stalled receiver cannot hold the request indefinitely. There is no unbounded producer queue.

### JSONL and multiple models (planned)

Load JSONL once at startup; reject malformed JSON, invalid UTF-8, missing/non-string/empty selected fields and empty datasets. Skip blank lines. Split strings by Unicode code points and preserve the text when chunks are joined; the final chunk may be shorter.

Each model will reserve its next record after validation, then release its cursor lock before waiting or writing. Records cycle in assignment order, not completion order. Cancellation consumes a record; restart resets the cursor. YAML dataset paths will resolve from the configuration directory. CLI paths will resolve from the working directory. CLI model options and `--config` will be mutually exclusive, with a listen-address override allowed. Neither `--config` nor `--dataset` is implemented yet.

## First real integration: LLM-LA Sidecar

Keep the existing Router/Redis setup. Point a real Sidecar at polyjuice-stream with a minimal text Chat Completions request and these overrides:

```sh
VLLM_URL=http://127.0.0.1:9900
MODEL_NAME=mock-model
MODEL_NAME_REDIS=mock-model
SCHED_ENABLED=false
TIMESHARE_ENABLED=false
MODELS_JSON=
STREAMING_MODE=true
FORCE_IGNORE_EOS=false
```

Use a reachable service address across containers. `VLLM_URL` has no `/v1` suffix because the Sidecar adds the request path. This is a proposed integration configuration, not a standalone Sidecar deployment or a verified test.

The current Sidecar checks `/health` and `/v1/models`, asks for `stream_options.include_usage`, and tolerates a missing trailing usage event. It requires a finish-reason event before `[DONE]`. Sleep/wake scheduling and legacy prompt-only requests need additional compatibility work.

## Implementation checks

| Stage | Acceptance example |
|---|---|
| Random source + HTTP | A fixed seed reproduces assigned text; SSE concatenation matches complete JSON for the same plan; every response finishes correctly. |
| Pacing + cancellation | Block the second write until 230ms: the next starts at or after 260ms. No chunks are dropped; disconnects release the request. Use a fake clock and writer. |
| JSONL + models (planned) | Records cycle 1, 2, 1; each model owns its cursor; Unicode round-trips; invalid input fails before SSE headers. |
| Field compatibility | Extra `user` is ignored; malformed core fields and unsupported `tools` return 400. |
| Container packaging (planned) | The executable and container serve the same configuration and pass the same HTTP smoke checks. |
| Real Sidecar (planned) | Start with one Sidecar. Then target 32 Sidecars and client concurrency 32/48/64/80/96/128, with 1000 chunks, a 30ms first delay and a 30ms interval (30s nominal). Compare direct and Router paths on recorded hardware. |

The integration load is a validation target, not a capacity claim. Record configured intervals separately from client-observed inter-chunk gaps; report chunks/s rather than tokenizer-accurate tokens/s.

## Reference implementations

- [vLLM incremental decoding](https://github.com/vllm-project/vllm/blob/main/vllm/v1/engine/detokenizer.py) and [output processing](https://github.com/vllm-project/vllm/blob/main/vllm/v1/engine/output_processor.py): why a text chunk is not necessarily one model token.

- [llm-d HTTP output](https://github.com/llm-d/llm-d-inference-sim/blob/main/pkg/communication/http.go): SSE encoding, flushing and errors.
- [llm-d datasets](https://github.com/llm-d/llm-d-inference-sim/blob/main/pkg/dataset/custom_dataset.go): separate selection from output formatting.
- [LiteLLM proxy streaming options](https://github.com/BerriAI/litellm/blob/main/litellm/proxy/common_request_processing.py): explicitly disable automatic usage requests in the gateway example.
