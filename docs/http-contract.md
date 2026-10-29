# v0.1 contract appendix

Stages 1 and 2 implement the HTTP API below for one model with random output or JSONL replay. Multi-model configuration remains planned. Start with the [visual design](design-v0.1.md) or [manual commands](manual-testing.md).

## Endpoints and fields

| Item | v0.1 behaviour |
|---|---|
| `POST /v1/chat/completions` | One assistant choice; text messages only. |
| `GET /v1/models` | Advertise the configured model name (`mock-model` by default). |
| `GET /health` | Ready after configuration and dataset validation. |
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

These are wire-format examples for three chunks, `"Hel"`, `"lo "` and `"LLM"`; IDs and timestamps vary. Start with `--dataset examples/datasets/responses.jsonl --chunk-chars 3` to select this text on the first request. Joining the deltas gives the complete response text. Restart resets replay to the first record; successive requests advance the cursor. LiteLLM may normalise its downstream envelope. Real vLLM streams incrementally decoded model output; it does not split at a fixed character count, and a text delta can combine multiple tokens.

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

### JSONL replay

`--dataset PATH` selects a local JSONL file; relative paths resolve from the working directory. `--text-field result` is the default. The field name is an exact top-level key: `--text-field output` reads `{"output":"Hello"}`, and `--text-field answer.text` reads a literal `"answer.text"` key, not a nested object. There is no field autodetection.

Load and validate every record once, before opening the listening socket. Skip blank lines, accept CRLF and a final line without a newline, and preserve selected text including whitespace. Reject malformed JSON, invalid UTF-8 anywhere in a record, unpaired surrogate escapes in the selected text, missing/null/non-string/empty selected fields, empty datasets and exceeded limits. Unselected metadata is ignored after JSON and UTF-8 validation. Errors identify the file and physical line, including skipped blank lines; they do not print record contents. Regular files are required.

| Limit | Maximum |
|---|---|
| Whole file, including blank lines and line endings | 64 MiB |
| JSONL line, excluding its line ending | 8 MiB |
| Records | 100,000 |
| Decoded text per record | 4 MiB |
| Chunks per record | 100,000 |
| `--chunk-chars` | 4,096 Unicode code points |

Output length comes from the selected record; `--chunks` and `--seed` cannot be supplied with `--dataset`. Split text by Unicode code points, never inside a UTF-8 encoding. The final chunk may be shorter. Multi-code-point emoji and combining sequences may span chunks; joining them preserves the original string without normalisation. Client output limits apply to these chunks after selection.

After request validation, reserve the next record under a short cursor lock and release it before chunking, waiting or writing. There is no request-time file access or JSON parsing. Records cycle in assignment order, not completion order, and SSE/JSON share that order. Cancellation consumes a claimed record; invalid requests consume none. Restart resets the cursor. Editing or deleting the file does not affect an already loaded server.

### Multiple models (planned)

Each model will own an independent source and cursor. YAML dataset paths will resolve from the configuration directory. CLI model options and `--config` will be mutually exclusive, with a listen-address override allowed. `--config` is not implemented yet.

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
| JSONL replay | Records cycle 1, 2, 1 across both modes; Unicode round-trips; cancellation consumes a claimed record; bad data fails before listening. |
| Multiple models (planned) | Each model owns its source and cursor; requests select the named model. |
| Field compatibility | Extra `user` is ignored; malformed core fields and unsupported `tools` return 400. |
| Container packaging (planned) | The executable and container serve the same configuration and pass the same HTTP smoke checks. |
| Real Sidecar (planned) | Start with one Sidecar. Then target 32 Sidecars and client concurrency 32/48/64/80/96/128, with 1000 chunks, a 30ms first delay and a 30ms interval (30s nominal). Compare direct and Router paths on recorded hardware. |

The integration load is a validation target, not a capacity claim. Record configured intervals separately from client-observed inter-chunk gaps; report chunks/s rather than tokenizer-accurate tokens/s.

## Reference implementations

- [vLLM incremental decoding](https://github.com/vllm-project/vllm/blob/main/vllm/v1/engine/detokenizer.py) and [output processing](https://github.com/vllm-project/vllm/blob/main/vllm/v1/engine/output_processor.py): why a text chunk is not necessarily one model token.

- [llm-d HTTP output](https://github.com/llm-d/llm-d-inference-sim/blob/main/pkg/communication/http.go): SSE encoding, flushing and errors.
- [llm-d datasets](https://github.com/llm-d/llm-d-inference-sim/blob/main/pkg/dataset/custom_dataset.go): separate selection from output formatting.
- [LiteLLM proxy streaming options](https://github.com/BerriAI/litellm/blob/main/litellm/proxy/common_request_processing.py): explicitly disable automatic usage requests in the gateway example.
