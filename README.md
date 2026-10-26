# Ghost Data Still Exists - deletion-audit agent

HackerRank Orchestrate (October 2026). For every request in `dataset/deletion_requests.csv` the system decides whether the person really asked for deletion, reconstructs their current and historical identifiers, establishes the state of every related record **at `audit_at`** from the supplied evidence, applies `deletion_rules.csv`, and writes `output.csv` with the findings, the ordered remediation plan and the verification checks. The challenge contract is in [`problem_statement.md`](./problem_statement.md) and [`AGENTS.md`](./AGENTS.md).

Written in **Go** with **SQLite**; **Gemini** is used for exactly two jobs: reading images/PDFs and judging free-text intent.

## Results (details in `code/evaluation/`)

| | |
|---|---|
| Sample status accuracy (32 public cases) | **100%** |
| Step precision / recall | 93.8% / 93.3% |
| Field accuracy on matched steps | 98.7% |
| `output.csv` | 518 rows, passes the submission-contract validator |
| Full run cost | about $0.19 (estimate), ~283k tokens, 422 model calls |

## Architecture

```
dataset/*.csv ──► dynamic CSV loader ─┐
dataset/evidence/                      │      SQLite (data/audit.db, rebuilt every run)
  .sql  ─ executed in a scratch DB ────┤        raw tables  +  curated relational layer
  .json ─ walked generically ──────────┤        users · identifiers (validity windows)
  .log  ─ logfmt / JSON-lines ─────────┼──►     evidence_documents/snapshots/records/fields
  .txt  ─ pipe-delimited ──────────────┤        record_identifiers · resource_links · resources
  .png/.pdf ─ Gemini vision (cached) ──┘
                         │
   request_text ─► Gemini intent classifier (cached) ─► NOT_APPLICABLE  (steps_json = [])
                         │ authorizes deletion
                         ▼
   identity resolution (current + historical email/phone/hash) → candidate resources
   evidence cut at audit_at → latest eligible file per resource → state (present/absent/exposed/...)
   deletion_rules.csv → findings → remediation (flows, restores, then records) → verification
   deterministic ordering + citations → output.csv → validator
```

Design principle: **deterministic Go and SQL do everything that can be computed; the LLM only does what needs judgement** (pixels and language). That keeps the run reproducible, cheap, and auditable - every evidence id in the output was found by a query, never invented by a model.

## Setup

Requirements: Go 1.26+ (module `ghostaudit`, pure-Go SQLite driver - no C compiler needed), and a Gemini API key.

```bash
cp code/.env.example .env      # at the repository root; then put your key in .env
```

`.env` (never committed; real environment variables take precedence):

| Variable | Purpose |
|---|---|
| `GEMINI_API_KEY` | required for vision and intent |
| `GEMINI_VISION_MODEL`, `GEMINI_MODEL` | model for images/PDFs, model for intent (default `gemini-3.5-flash-lite` if unset; set the model your key can use) |
| `GEMINI_CONCURRENCY`, `INTENT_CONCURRENCY` | parallel requests (default 4 / 8) |
| `GEMINI_PRICE_INPUT_PER_M`, `GEMINI_PRICE_OUTPUT_PER_M` | USD per 1M tokens, used only for the cost estimate |

## Run

All commands run from `code/` (they find the repository root themselves, so they also work from the root):

```bash
cd code
go run ./cmd/pipeline          # ingest + vision + intent + sample audit + full audit -> ../output.csv
go run ./cmd/eval              # score the 32 public samples against dataset/sample_output.csv
go run ./cmd/validate          # check ../output.csv against the submission contract
go run ./cmd/report -run <id>  # regenerate evaluation/evaluation_report.md and usage_report.md
go test ./...                  # unit tests + the custom evaluation cases
```

Individual stages: `go run ./cmd/ingest` (database only), `go run ./cmd/audit [--sample]` (predictions only), `go run ./cmd/sql "<query>"` (read-only query of the database; open `data/audit.db` in DBeaver for exploration).

Safety behaviour: if a Gemini call fails the run **stops** rather than quietly writing a weaker answer (`--allow-degraded` overrides, `--no-llm` / `--no-vision` run offline). LLM results are cached in `data/cache.db` by content hash + model + prompt version, and the cache is backed up before every run, so a re-run costs nothing.

## Where things are

| Path | What |
|---|---|
| `code/internal/store/schema.go` | the whole relational data model (identity graph, evidence tables, links, views) |
| `code/internal/store/csvload.go` | dynamic CSV loader: tables, keys and indexes come from the headers |
| `code/internal/evidence/` | extractors per format (SQL, JSON, log, text), field flattening/classification, Gemini vision prompt and cache |
| `code/internal/intent/intent.go` | the intent prompt, classifier, cache, multilingual fallback |
| `code/internal/audit/` | the engine: `engine.go` (subjects, candidates, observations), `facts.go` (record shapes -> facts), `plan.go` (state, rules, steps, ordering, citations), `attribution.go` (worker-activity matching), `rules.go` (editable configuration) |
| `code/internal/validate/`, `code/internal/score/` | contract validator, scoring |
| `code/evaluation/` | `evaluation_report.md`, `usage_report.md` |
| `code/interview/` | `index.html` - the flow diagram and the reasoning behind each choice |
| `code/tools/` | read-only Python scripts used during the investigation of the dataset |

## Dynamic by design

Nothing about the data is hard-coded except a small, documented configuration (`internal/audit/rules.go`): CSVs become tables from their headers; evidence files are discovered by walking `dataset/evidence/` (not only the manifest); SQL snapshots are read by enumerating whatever tables they contain; JSON is walked generically; vision prompts list the systems found in the data; rule actions come from `deletion_rules.csv`. A new evidence file, a new table or a new column needs no code change.

## Time rules

Only evidence **captured at or before `audit_at`** may be used or cited, and a record's state comes from the **latest eligible file** that observes it. Later evidence is never used for state or citation (custom evaluation case 1 enforces this). Workflow statuses and activity results are treated as claims; they are used only to *attribute* resources that carry no identifier, never as proof of state.

## Known limits

See section 6 of `code/evaluation/evaluation_report.md`: some retained audit-record ids appear in no supplied file, and cases that share a `reported_at` can compete for the same unidentified resource.
