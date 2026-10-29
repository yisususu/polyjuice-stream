# Polyjuice Stream v0.1

> Stages 1 and 2 are implemented: one model with random output or JSONL replay, configurable timing, SSE and complete JSON. Multiple models, containers and the integrations below remain planned.

Turn random text or recorded outputs into a callable model service with one command. Save accelerator resources while testing gateways, routers and clients.

## 1. Start and connect

![Executable or container, same service](../assets/images/01-start.png)

Run the Go executable now. The dashed container path is planned; it will expose the same API.

```sh
# Build once, then run with zero configuration
make build
./bin/polyjuice-stream serve

# Reproduce the original 30-second workload
./bin/polyjuice-stream serve --chunks 1000 --first-delay 30ms --interval 30ms
```

The following commands are **planned**, not available yet:

```sh
# Multiple model profiles
polyjuice-stream serve --config examples/configs/multi-model.yaml

# Container packaging (proposed local image name)
docker run --rm -p 127.0.0.1:9900:9900 \
  -v "$PWD/examples:/examples:ro" polyjuice-stream:local serve \
  --listen 0.0.0.0:9900 --config /examples/configs/multi-model.yaml
```

Today, flags control one model. Choose random output with `--chunks 100`, or replay with `--dataset examples/datasets/responses.jsonl`. Both share `--model`, `--chunk-chars`, `--first-delay` and `--interval`. The future [model configuration](../examples/configs/multi-model.yaml) gives multiple models independent settings.

## 2. One plan, two response modes

![v0.1 architecture](../assets/images/02-architecture.png)

One model profile selects either a random source or an in-memory JSONL source. The diagram also shows planned multi-model selection. At startup, load and validate the entire dataset before listening. Each valid request claims a record, then builds the same response plan used for random output. SSE and complete JSON share its text and generation waits; blocked writes can extend the actual duration.

**First integration:** LLM-LA with its real Sidecar, initially in single-model mode with switching disabled. LiteLLM below demonstrates the ordinary HTTP boundary. Engine-specific sleep/wake controls remain outside this first integration.

## 3. Replay by example

![Independent model profiles and cyclic replay](../assets/images/03-replay.png)

Our [sample dataset](../examples/datasets/responses.jsonl) contains:

```jsonl
{"result":"Hello LLM"}
{"result":"Next text"}
```

```sh
./bin/polyjuice-stream serve --model replay-model \
  --dataset examples/datasets/responses.jsonl --chunk-chars 3
```

`replay-model` selects record 1, record 2, then record 1 again, regardless of prompt. A cancelled request consumes its claimed record; an invalid request consumes none. Both response modes share the cursor. Separate requests can therefore return different text—even when only `stream` changes. The diagram's simultaneous model profiles are a future capability.

Use `--text-field output` to select another top-level key. Missing fields, malformed JSON, invalid Unicode and size-limit failures stop startup with the file and line number. [Manual checks](manual-testing.md) include Chinese, emoji and cancellation examples.

## 4. Send a request through LiteLLM (integration target)

![LiteLLM request and response sequence](../assets/images/04-litellm.png)

Validate core fields; ignore unrelated extras such as `user`. Explicitly unsupported features such as `tools` return 400 before streaming starts.

Start the replay server from the previous section. This gateway integration has not been verified yet; its proposed [LiteLLM configuration](../examples/configs/litellm.yaml) is:

```yaml
model_list:
  - model_name: demo
    litellm_params:
      model: openai/replay-model
      api_base: http://127.0.0.1:9900/v1
      api_key: unused

general_settings:
  always_include_stream_usage: false
```

```sh
litellm --config examples/configs/litellm.yaml --port 4000
curl -N http://127.0.0.1:4000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"demo","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

LiteLLM maps `demo` to backend model `replay-model`. With `stream: true` and `chunk_chars: 3`, the first record arrives as `"Hel"`, `"lo "`, `"LLM"`, then a finish event and `[DONE]`. These are configured mock chunks, not vLLM token boundaries. With `stream: false`, it arrives as one assistant message. See [exact mock response examples](http-contract.md#response-examples).

Loopback addresses assume the two services share the host network namespace. Containers on a shared network should use the mock's service name. This example follows [LiteLLM's OpenAI-compatible backend configuration](https://docs.litellm.ai/docs/providers/openai_compatible).

## 5. Timing and cancellation

![Ideal timing without blocked writes, and cancellation](../assets/images/05-timing.png)

With negligible processing and no blocked writes, three chunks have nominal times of **100, 130 and 160ms**. Non-streaming uses the same generation waits, then returns the full text. Times start after planning.

![Blocked sends extend the duration without catch-up](../assets/images/06-backpressure.png)

**No catch-up, no dropped content.** After each successful write and flush, wait at least one interval. If the second write finishes at 230ms, the third starts no earlier than 260ms. These are server timings; network buffering can still group client arrivals. Cancellation stops only that request.

One output unit is one text chunk, not a real model token. `max_tokens: 2` caps a request at two chunks and finishes with `length` when content was truncated. In the replay example, it returns `"Hello "` from the first record. Chunking uses Unicode code points, so joining uncapped deltas restores the original Chinese or emoji text.

## Build and check

**Current:** random or JSONL source + both response modes + local executable. **Next:** multiple models → container packaging → real Sidecar integration. [Manual commands](manual-testing.md) and [acceptance checks](http-contract.md#implementation-checks) cover replay, pacing, cancellation and compatibility.

Keep the release small: text Chat Completions, `/v1/models` and `/health`. The [contract appendix](http-contract.md) holds field rules and integration notes; the [layout guide](repository-layout.md) explains source folders.

PNG figures are published in `assets/images/`. Editable diagram sources and SVG exports are kept locally.
