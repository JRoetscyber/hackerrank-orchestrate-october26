# Software Development Life Cycle (SDLC): Ghost Data Audit Pipeline

**Project:** HackerRank Orchestrate Hackathon (`hackerrank-orchestrate-october26`)  
**Language & Stack:** Go (Golang) + SQLite + LLM API (Structured Outputs)  
**Core Philosophy:** **Deterministic Go First, AI Second.** Go and SQLite do 85% of the work (ingestion, identity resolution, evidence hunting, timestamp filtering, and sorting). The LLM only handles the 15% that requires fuzzy human-like judgment (intent classification, unstructured image/PDF reading, and final JSON synthesis).

---

## 1. Requirements & Problem Analysis (Phase 1)

### 1.1 What We Are Actually Building
We are **not** deleting data ourselves. We are building an **automated forensic auditor** that looks at a snapshot of a system at a specific timestamp (`audit_at`) for **518 cases** (`deletion_requests.csv`) and outputs a single `output.csv` file.

### 1.2 Inputs
1. **`deletion_requests.csv`**: The 518 cases (`case_id`, `user_identifier`, `request_text`, `requested_at`, `audit_at`).
2. **`user_identities.csv` & `user_audit_log.csv`**: Current and historical user data (emails, phone numbers, hashes, mapped to `user_id`).
3. **`deletion_rules.csv`**: The compliance rulebook explaining what *should* happen to each data type (e.g., hard delete, anonymize, tombstone, or retain).
4. **`system_activity.csv`**: Global logs of sync jobs, backups, restores, and deletions.
5. **`evidence_files.csv` & `evidence/`**: Raw evidence files (`.sql` dumps, `.json`, `.log`, `.pdf`, `.png` screenshots).
6. **`sample_deletion_requests.csv` & `sample_output.csv`**: 32 ground-truth test cases used to benchmark our accuracy.

### 1.3 Outputs & Deliverables
1. **`output.csv`**: Columns `case_id,deletion_status,steps_json` sorted by `case_id`.
   - `deletion_status`: `NOT_APPLICABLE`, `COMPLETE`, or `INCOMPLETE`.
   - `steps_json`: Ordered array of findings, remediation actions, and verification checks citing exact `evidence_id`s.
2. **`code/evaluation/evaluation_report.md`**: Accuracy against the 32 sample cases + 3 custom test cases.
3. **`code/evaluation/usage_report.md`**: Token counts, LLM calls, latency, and cost tracking.
4. **`log.txt`**: Automated log of all AI coding assistant prompts/responses.

---

## 2. System Architecture & Division of Labor (Phase 2)

```
+-------------------------------------------------------------------------+
|                        STAGE 0: SQLite Ingestion                        |
|  Load all CSVs + Parse .sql/.json/.log evidence into indexed SQLite DB  |
+-------------------------------------------------------------------------+
                                    |
                                    v
+-------------------------------------------------------------------------+
|                    STAGE 1: Intent Check (Go + AI)                      |
|  Does request_text actually ask for account/data deletion?              |
|  --> If NO: Output status = NOT_APPLICABLE, steps_json = [] (DONE)      |
+-------------------------------------------------------------------------+
                                    | (If YES)
                                    v
+-------------------------------------------------------------------------+
|             STAGE 2: Identity Graph Resolution (100% Go/SQL)            |
|  Query user_identities & user_audit_log to collect ALL historical:      |
|  [user_id, current_email, old_emails, phone_numbers, email_hashes]      |
+-------------------------------------------------------------------------+
                                    |
                                    v
+-------------------------------------------------------------------------+
|              STAGE 3: Deterministic Evidence Hunt (100% Go)             |
|  1. Search SQLite & text evidence files for ANY identifier from Stage 2 |
|  2. Filter system_activity.csv for events <= audit_at                   |
|  3. Detect Sync/Restore risks (e.g., backup taken before deletion       |
|     restored or scheduled to sync after deletion)                       |
+-------------------------------------------------------------------------+
                                    |
                                    v
+-------------------------------------------------------------------------+
|          STAGE 4: Rule Evaluation & Plan Synthesis (Go + AI)            |
|  Pass compact "Case Dossier" (Identifiers + Matched Evidence + Rules)   |
|  to LLM to generate structured steps_json & COMPLETE/INCOMPLETE status  |
+-------------------------------------------------------------------------+
                                    |
                                    v
+-------------------------------------------------------------------------+
|             STAGE 5: Validation, Sorting & Export (100% Go)             |
|  Validate JSON schema, enforce strict step ordering, log token usage,   |
|  and write sorted rows to output.csv                                    |
+-------------------------------------------------------------------------+
```

### Why This Architecture Wins
* **Zero Hallucinated Evidence IDs:** The AI never searches the raw files itself. Go finds the exact `evidence_id`s and hands them to the AI on a silver platter.
* **Blazing Fast Execution:** SQLite queries take milliseconds per case. Parallelizing 518 cases across Go worker routines (`goroutines`) will finish the entire run in under 2–3 minutes.
* **Minimal Token Cost:** Instead of stuffing megabytes of CSVs into an LLM prompt, each LLM call only receives the 5–10 lines of evidence that actually matched that specific user.

---

## 3. Detailed Stage-by-Stage Design (Phase 3)

### Stage 0: SQLite Database Initialization (`internal/db`)
Instead of writing complex Go loops to join CSVs in memory, we spin up a local SQLite database (`audit.db`) on startup:
* **Tables to create directly from CSVs:**
  * `deletion_requests`
  * `user_identities` (Index on `user_id`, `email`, `phone`, `email_hash`)
  * `user_audit_log` (Index on `user_id`, `old_value`, `new_value`)
  * `deletion_rules`
  * `system_activity` (Index on `user_id`, `timestamp`, `system_name`)
  * `evidence_files` (Index on `evidence_id`, `system_name`, `file_path`)
* **Evidence Indexing (`evidence_Index` table):**
  * For every text-based file in `dataset/evidence/` (`.sql`, `.json`, `.log`, `.txt`), Go reads the file line-by-line and inserts `(evidence_id, line_number, raw_content)` into an SQLite FTS5 (Full-Text Search) table.
  * For binary files (`.pdf`, `.png`), we extract text once (via a lightweight parser or single vision/OCR pass) and cache it in the same table so we never process an image twice.

### Stage 1: Intent Filter (`internal/pipeline/intent.go`)
* **What needs to be checked:** Look at `request_text` in `deletion_requests.csv`. Users sometimes submit support tickets asking "How do I change my password?" or "Cancel my subscription" (not data deletion).
* **How it works:**
  * **Fast Regex Pre-check (Go):** Check for obvious non-deletion or obvious deletion phrases if applicable, or send `request_text` to a fast, cheap LLM prompt returning `{ "is_deletion_request": bool, "reason": string }`.
  * If `false`, immediately mark `NOT_APPLICABLE` and skip Stages 2–4.

### Stage 2: Identity Resolution (`internal/pipeline/identity.go`)
* **What needs to be checked:** A user might submit a deletion request using `old_email@example.com`, while the database stores records under `user_9942` or `new_email@example.com`.
* **How it works (Pure SQL in Go):**
  1. Take the input identifier from the request.
  2. Query `user_identities` and `user_audit_log` where any column matches the input identifier.
  3. Once `user_id` is found, run a second query to pull **every historical value** ever associated with that `user_id` (up to `audit_at`).
  4. Return a Go struct:
     ```go
     type UserProfile struct {
         UserID      string
         Emails      []string
         Phones      []string
         EmailHashes []string
     }
     ```

### Stage 3: Evidence & Sync Risk Hunter (`internal/pipeline/hunter.go`)
* **What needs to be checked:** Where does this user's data still exist at `audit_at`, and is a background job about to bring it back?
* **How it works (Pure Go + SQL):**
  1. **Search Evidence:** Query our `evidence_index` table for any occurrence of `UserID`, `Emails`, `Phones`, or `EmailHashes` (only in evidence files captured at or before `audit_at`).
  2. **Search System Activity:** Query `system_activity` for this `UserID` (or system-wide batch jobs on systems where the user had data) where `timestamp <= audit_at`.
  3. **Detect Ghost / Sync Anomalies:**
     * *Replica Lag / Unwiped Storage:* Primary DB shows deleted, but an evidence file for `replica_db` or `search_index` still contains the user's email.
     * * Zombie Restore Risk:* Deletion happened at `10:00`, but `system_activity` shows a backup from `08:00` being restored or synced at `11:00`.
  4. Package all hits into a `CaseDossier` struct containing exact `evidence_id`s, snippets, system names, and applicable rows from `deletion_rules.csv`.

### Stage 4: Rule Evaluation & `steps_json` Generation (`internal/agent`)
* **What needs to be checked:** Compare every piece of found data against `deletion_rules.csv` to decide if its presence is a violation (`INCOMPLETE`) or compliant (`COMPLETE`—for example, a legally retained billing record or properly anonymized tombstone).
* **How it works (Go + LLM Structured Output):**
  * Go constructs a strict prompt containing:
    * The `sample_output.csv` examples (few-shot prompting so the AI copies the exact style and naming conventions).
    * The `CaseDossier` (User identifiers, matched `evidence_id`s + snippets, `system_activity` timeline, and `deletion_rules`).
  * The LLM returns a strict JSON object matching the required `steps_json` schema.
  * **Token Tracker Middleware:** Every LLM call automatically increments atomic counters in Go (`totalCalls`, `promptTokens`, `completionTokens`, `estimatedCostUSD`) so `usage_report.md` can be generated automatically.

### Stage 5: Post-Processing & Deterministic Sorting (`internal/pipeline/ formatting.go`)
* **What needs to be checked:** HackerRank's grader is strict about JSON schema and step ordering.
* **How it works (Pure Go):**
  * Unmarshal the LLM's JSON into Go structs to verify every `evidence_id` cited actually exists in `evidence_files.csv`.
  * Sort the `steps_json` array deterministically according to the problem statement rules (Findings -> Remediation Actions -> Verification Checks).
  * Sort all 518 output rows by `case_id` and write `output.csv`.

---

## 4. Project Directory Structure (Phase 4: Implementation Plan)

```text
hackerrank-orchestrate-october26/
├── cmd/
│   ├── audit/
│   │   └── main.go              # CLI entrypoint (flags: --sample, --full, --workers=10)
│   └── eval/
│       └── main.go              # Compares our output against sample_output.csv
├── internal/
│   ├── db/
│   │   ├── sqlite.go            # Loads CSVs and evidence files into SQLite
│   │   └── sqlite_test.go       # Unit tests for CSV ingestion
│   ├── pipeline/
│   │   ├── identity.go          # Stage 2: User ID & historical alias lookup
│   │   ├── identity_test.go     # Unit tests for identity resolution
│   │   ├── hunter.go            # Stage 3: Evidence & sync-risk search
│   │   ├── hunter_test.go       # Unit tests for finding ghost data
│   │   └── formatter.go         # Stage 5: Deterministic sorting & CSV writer
│   └── agent/
│       ├── client.go            # LLM wrapper + automatic token/cost logger
│       └── prompts.go           # Few-shot prompt templates from sample_output.csv
├── code/
│   └── evaluation/
│       ├── evaluation_report.md # Auto-generated + custom test cases
│       └── usage_report.md      # Auto-generated token/cost metrics
├── output.csv                   # Final 518-row submission file
└── log.txt                      # AI coding assistant transcript
```

---

## 5. Testing & Evaluation Strategy (Phase 5)

### 5.1 Unit Tests (`go test ./...`)
We will write fast unit tests in Go for our deterministic stages:
1. **`identity_test.go`**: Pick 3 tricky users from `sample_deletion_requests.csv` who changed their emails in `user_audit_log.csv` and assert that `ResolveIdentity()` returns 100% of their old and current identifiers.
2. **`hunter_test.go`**: Assert that given a known `user_id` from the sample set, `HuntEvidence()` finds the exact same `evidence_id`s listed in `sample_output.csv`.
3. **`custom_cases_test.go`**: Write **3 custom synthetic test cases** (e.g., a race condition where a backup restore happens 1 minute before `audit_at`, a non-deletion support request, and a historical email hash match). This directly satisfies the `evaluation_report.md` requirement!

### 5.2 Ground-Truth Integration Evaluator (`cmd/eval/main.go`)
Before running on all 518 cases, we run our pipeline on the **32 sample cases** (`--sample` flag) and run our Go evaluation script against `sample_output.csv`. The script will calculate:
* **Status Accuracy (%):** Exact match on `COMPLETE` vs `INCOMPLETE` vs `NOT_APPLICABLE`.
* **Evidence ID Recall & Precision (%):** Did our Go hunter find the exact `evidence_id`s that `sample_output.csv` references?
* **Step Structure Match (%):** Does our `steps_json` match the expected action types and ordering?

We iterate on our Go SQL queries and LLM prompt until **Sample Accuracy hits 95–100%**, and only then do we run the full 518 cases.

---

## 6. Hour-by-Hour Execution Roadmap (Next 5 Hours)

* **Hour 1 (Data & SQLite):** Initialize Go module, load the CSVs into SQLite, and inspect 3–5 rows of `sample_output.csv` alongside their matching evidence files so we see the exact JSON structure.
* **Hour 2 (Identity & Evidence Hunter):** Write `identity.go` and `hunter.go`. Run a test script against the 32 sample cases to verify our Go code finds 100% of the `evidence_id`s mentioned in `sample_output.csv` without even calling the AI yet.
* **Hour 3 (LLM Agent & Prompt Engineering):** Hook up the LLM client with token tracking, feed it the `CaseDossier` + few-shot examples from `sample_output.csv`, and tune the prompt on the 32 sample cases.
* **Hour 4 (Full Run & Verification):** Run the pipeline across all 518 cases in `deletion_requests.csv` using concurrent Go workers. Validate `output.csv` format, headers, and row count (518 rows).
* **Hour 5 (Reports & Packaging):** Auto-generate `usage_report.md` and `evaluation_report.md`, verify `log.txt` is up to date, and zip the repository for submission.