# Usage report

Final full-dataset run that produced `output.csv`: run id `20261025T102249Z`, generated 2026-10-25 10:28 UTC.

The run executed `go run ./cmd/pipeline` from an empty cache (the vision pass was entirely live; see the Live calls column for intent calls, a few of which were cache hits because identical request texts recur): ingest with vision over the evidence images/PDFs, then intent classification of the 32 public sample requests (used for scoring) and the 518 evaluation requests. The audit engine itself (identity resolution, evidence selection, state derivation, rules, ordering) makes no model calls.

## Per-model, per-purpose totals

| Purpose | Provider | Model | Calls | Live calls | Input tokens | Output tokens | Total tokens | Est. cost (USD) | Avg latency (live) |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| intent_classify | google | `gemini-3.5-flash-lite` | 407 | 398 | 212164 | 22145 | 234309 | $0.1190 | 1119 ms |
| vision_extract | google | `gemini-3.5-flash-lite` | 15 | 15 | 25013 | 24072 | 49085 | $0.0677 | 5025 ms |
| **Overall** | | | **422** | **413** | **237177** | **46217** | **283394** | **$0.1867** | |

## Per-request figures

Evaluation requests in the dataset: **518**.

| Metric | Value |
|---|---:|
| Total tokens (input + output) | 283394 |
| Average tokens per request | 547.1 |
| Estimated total cost | $0.1867 |
| Estimated cost per request | $0.000360 |
| Model calls per request | 0.81 |

Totals include the one-off vision pass over the evidence files and the 32 sample requests, so the per-request average is conservative (it spreads all of that over the 518 evaluation requests only).

## How the numbers are produced

- Every model call is written to the `llm_usage` table (SQLite, `data/cache.db`) with run id, purpose, provider, model, input tokens, output tokens, latency and cost, using the `usageMetadata` the Gemini API returns. Output tokens include thinking tokens, which are billed as output.
- Cost is an **estimate**: tokens x price per million tokens, using `GEMINI_PRICE_INPUT_PER_M=0.30` and `GEMINI_PRICE_OUTPUT_PER_M=2.50` USD from the environment (defaults in `.env.example`). Check them against the current price list of the model used; the token counts are exact.
- Responses are cached by content hash (file hash for vision; request-text hash for intent) together with the model name and a prompt-version hash, so re-running with the same inputs costs nothing and changing the prompt or model invalidates the cache automatically.
- One model is used for both purposes (the vision model and the intent model are set separately with `GEMINI_VISION_MODEL` / `GEMINI_MODEL`); per-model totals are the rows above.
