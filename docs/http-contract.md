# v0.1 contract appendix

Design reference, not an implemented API. Start with the [visual design](design-v0.1.md).

## Endpoints and fields

| Item | v0.1 behaviour |
|---|---|
| `POST /v1/chat/completions` | One assistant choice; text messages only. |
| `GET /v1/models` | Advertise every configured model name. |
| `GET /health` | Ready after configuration and dataset validation. |
| `model`, `messages` | Require a known model and a non-empty array of messages with string content. Prompt contents do not choose the response. |
| `stream` | Defaults to false; both modes share content planning and timing. |
| `max_tokens`, `max_completion_tokens` | Positive simulated-chunk limits. Equal values are accepted together; conflicting values return 400. |
| `temperature`, `top_p`, penalties | Accepted and ignored; this is content playback, not sampling a model. |
| `stream_options.include_usage` | Accepted for Sidecar compatibility, but v0.1 omits usage accounting. Other stream options are unsupported. |
| `n` | Omitted or 1 only. |
| Tools, multimodal input, structured output, `stop` | Unsupported: return a clear 400 error. |
| Unrelated extension fields, e.g. `user` | Accept and ignore. Core fields and their types remain validated; explicitly unsupported features and stream options still return 400. |

Unknown models return 404. Validation errors return JSON with `error.message`, `error.type`, `error.param` where applicable and `error.code`. Validate before starting SSE; after headers are sent, transport failure terminates the stream.

Use `finish_reason: length` only if the client limit truncates planned content; otherwise use `stop`. Do not represent chunk counts as tokenizer-accurate usage.

## Response examples

These are exact example bodies from the mock for one selected record, `Hello LLM`, with three-character chunks. IDs and timestamps are illustrative. LiteLLM may normalise its downstream envelope. The split follows `chunk_chars: 3`. Real vLLM streams incrementally decoded model output; it does not split at a fixed character count, and a text delta can combine multiple tokens.

### SSE (`Content-Type: text/event-stream`)

```text
data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":1790985600,"model":"replay-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}

data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":1790985600,"model":"replay-model","choices":[{"index":0,"delta":{"content":"lo "},"finish_reason":null}]}

data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":1790985600,"model":"replay-model","choices":[{"index":0,"delta":{"content":"LLM"},"finish_reason":null}]}

data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":1790985600,"model":"replay-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

```

The first role and content share an event. Flush each content event. Metadata and termination events do not count toward output limits.

### Complete JSON (`Content-Type: application/json`)

```json
{
  "id": "chatcmpl-example",
  "object": "chat.completion",
  "created": 1790985600,
  "model": "replay-model",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "Hello LLM"},
    "finish_reason": "stop"
  }]
}
```

## Data and timing rules

- Split strings by Unicode code points, preserving text when chunks are joined. The final chunk may be shorter. This is not model tokenisation.
- Random output uses printable ASCII for stable chunk byte sizes. An optional per-model `seed` reproduces the text sequence in server request-assignment order; omit it for a fresh seed at startup. Concurrent arrival order can change which client receives each text.
- Load JSONL once at startup. Report file/line errors for malformed JSON, invalid UTF-8, missing/non-string/empty selected fields or an empty dataset. Skip blank lines. Choose file and record size limits during implementation.
- Each model reserves its next record after request validation. Release the cursor lock before waiting or writing. Assignment order is not completion order; cancellation consumes a record; restart resets the cursor.
- Resolve YAML dataset paths from the configuration directory. CLI paths resolve from the working directory. CLI model options and `--config` are mutually exclusive; a listen-address override is allowed.
- After planning, wait `first_delay` before the first content chunk. After each successful write and flush, wait at least `interval` before starting the next chunk, using monotonic time. A blocked send extends the duration. Never catch up with a burst or drop content. Use cancellable waits and bounded write deadlines.
- Non-streaming uses the same generation waits without intermediate network writes, then returns the joined text. Both modes have the same nominal duration; only streaming has per-chunk transport stalls. Server flush completion is not proof of client receipt.
- Stop on disconnect or a write/flush error. No unbounded producer queue. Check incremental delivery, concurrent model independence and cancellation in integration tests.

## First real integration: LLM-LA Sidecar

Keep the existing Router/Redis setup. Point a real Sidecar at polyjuice-stream with a minimal text Chat Completions request and these overrides:

```sh
VLLM_URL=http://127.0.0.1:8000
MODEL_NAME=replay-model
MODEL_NAME_REDIS=replay-model
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
| JSONL + models | Records cycle 1, 2, 1; each model owns its cursor; Unicode round-trips; invalid input fails before SSE headers. |
| Field compatibility | Extra `user` is ignored; malformed core fields and unsupported `tools` return 400. |
| Packaging | The executable and container serve the same configuration and pass the same HTTP smoke checks. |
| Real Sidecar | Start with one Sidecar. Then target 32 Sidecars and client concurrency 32/48/64/80/96/128, with 1000 chunks, a 30ms first delay and a 30ms interval (30s nominal). Compare direct and Router paths on recorded hardware. |

The integration load is a validation target, not a capacity claim. Record configured intervals separately from client-observed inter-chunk gaps; report chunks/s rather than tokenizer-accurate tokens/s.

## Reference implementations

- [vLLM incremental decoding](https://github.com/vllm-project/vllm/blob/main/vllm/v1/engine/detokenizer.py) and [output processing](https://github.com/vllm-project/vllm/blob/main/vllm/v1/engine/output_processor.py): why a text chunk is not necessarily one model token.

- [llm-d HTTP output](https://github.com/llm-d/llm-d-inference-sim/blob/main/pkg/communication/http.go): SSE encoding, flushing and errors.
- [llm-d datasets](https://github.com/llm-d/llm-d-inference-sim/blob/main/pkg/dataset/custom_dataset.go): separate selection from output formatting.
- [LiteLLM proxy streaming options](https://github.com/BerriAI/litellm/blob/main/litellm/proxy/common_request_processing.py): explicitly disable automatic usage requests in the gateway example.
