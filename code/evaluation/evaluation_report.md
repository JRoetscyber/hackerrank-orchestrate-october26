# Evaluation report

Generated 2026-10-25 10:28 UTC by `go run ./cmd/report`. Reproduce every number here with:

```
cd code
go run ./cmd/pipeline      # ingest + vision + intent + audit -> ../output.csv
go run ./cmd/eval           # score the 32 public samples
go run ./cmd/validate       # check output.csv against the submission contract
go test ./...               # custom evaluation cases
```

## 1. Accuracy on the 32 public sample requests

Predictions (`data/sample_pred.csv`) are produced by the same code path as the 518-case run and scored against `dataset/sample_output.csv`. The answer key is used **only** for scoring, never for prediction.

| Metric | Result | How it is computed |
|---|---:|---|
| Case-level deletion-status accuracy | **100.0%** (32/32) | `deletion_status` equals the key |
| Step-level precision | 93.8% | predicted steps whose (phase, system, resource, operation) exist in the key |
| Step-level recall | 93.3% | key steps found in the prediction |
| Step-level F1 | 93.5% | harmonic mean |
| Field-level accuracy | 98.7% | on matched steps: `state`, `evidence_id`, `step_no` (3 fields per step) |
| Steps correct on every field | 89.6% (173/193) | identity + state + evidence + position |
| Sequence accuracy (exact) | 62.5% (20/32 cases) | whole ordered `steps_json` identical, status identical |
| Sequence similarity | 92.1% | mean LCS ratio of the ordered step lists (partial credit for order) |

Row-level accuracy (status and steps both exactly equal) is the exact-sequence row. The public samples contain no `NOT_APPLICABLE` cases, so intent handling is evaluated separately in section 3.

### Cases that are not identical

| Case | Gold / predicted status | Differences |
|---|---|---|
| case_03 | COMPLETE / COMPLETE | MISSING  #3 VERIFICATION/analytics_platform/analytics_9406/VERIFY_RECORD_ANONYMIZED ev="evidence_94"<br>EXTRA    #3 VERIFICATION/analytics_platform/analytics_5589/VERIFY_RECORD_ANONYMIZED ev="evidence_94" |
| case_04 | COMPLETE / COMPLETE | MISSING  #1 VERIFICATION/account_service/account_004/VERIFY_RECORD_ABSENT ev="evidence_54"<br>DIFF     VERIFICATION/analytics_platform/analytics_901/VERIFY_RECORD_ANONYMIZED gold(no=2 ev="evidence_56") pred(no=1 ev="evidence_56") |
| case_07 | INCOMPLETE / INCOMPLETE | MISSING  #1 FINDING/support_platform/support_001/REPORT_REMAINING_RECORD ev="evidence_58"<br>MISSING  #2 REMEDIATION/support_platform/support_001/DELETE_RECORD ev=""<br>MISSING  #4 VERIFICATION/support_platform/support_001/VERIFY_RECORD_ABSENT ev=""<br>EXTRA    #1 FINDING/support_platform/support_001/REPORT_EXPOSED_PII ev="evidence_58"<br>EXTRA    #2 REMEDIATION/support_platform/support_001/REDACT_RECORD ev=""<br>EXTRA    #4 VERIFICATION/support_platform/support_001/VERIFY_RECORD_REDACTED ev="" |
| case_09 | INCOMPLETE / INCOMPLETE | DIFF     VERIFICATION/backup_service/backup_4978/VERIFY_RESTORE_BLOCKED gold(no=5 ev="") pred(no=6 ev="")<br>EXTRA    #5 VERIFICATION/analytics_platform/analytics_5718/VERIFY_RECORD_ANONYMIZED ev="" |
| case_18 | INCOMPLETE / INCOMPLETE | DIFF     VERIFICATION/analytics_platform/analytics_037/VERIFY_RECORD_ANONYMIZED gold(no=13 ev="") pred(no=12 ev="")<br>DIFF     VERIFICATION/integration_service/data_flow_4890/VERIFY_DATA_FLOW_DISABLED gold(no=14 ev="") pred(no=13 ev="")<br>MISSING  #11 VERIFICATION/security_service/audit_record_6645/VERIFY_RECORD_PRESENT ev=""<br>DIFF     VERIFICATION/support_platform/support_5569/VERIFY_RECORD_REDACTED gold(no=12 ev="") pred(no=11 ev="") |
| case_20 | INCOMPLETE / INCOMPLETE | MISSING  #7 VERIFICATION/security_service/audit_record_5569/VERIFY_RECORD_PRESENT ev=""<br>EXTRA    #7 VERIFICATION/analytics_platform/analytics_2265/VERIFY_RECORD_ANONYMIZED ev="" |
| case_23 | INCOMPLETE / INCOMPLETE | MISSING  #5 VERIFICATION/account_service/account_410/VERIFY_RECORD_ABSENT ev=""<br>EXTRA    #5 VERIFICATION/account_service/account_9155/VERIFY_RECORD_ABSENT ev="" |
| case_24 | INCOMPLETE / INCOMPLETE | EXTRA    #8 VERIFICATION/analytics_platform/analytics_4566/VERIFY_RECORD_ANONYMIZED ev="" |
| case_26 | COMPLETE / COMPLETE | MISSING  #4 VERIFICATION/analytics_platform/analytics_4566/VERIFY_RECORD_ANONYMIZED ev="evidence_92"<br>DIFF     VERIFICATION/backup_service/backup_3120/VERIFY_RESTORE_BLOCKED gold(no=6 ev="evidence_92") pred(no=5 ev="evidence_92")<br>DIFF     VERIFICATION/integration_service/data_flow_6244/VERIFY_DATA_FLOW_DISABLED gold(no=5 ev="evidence_92") pred(no=4 ev="evidence_92") |
| case_27 | COMPLETE / COMPLETE | MISSING  #4 VERIFICATION/analytics_platform/analytics_4574/VERIFY_RECORD_ANONYMIZED ev="evidence_92"<br>EXTRA    #4 VERIFICATION/analytics_platform/analytics_6077/VERIFY_RECORD_ANONYMIZED ev="evidence_92" |
| case_28 | COMPLETE / COMPLETE | MISSING  #1 VERIFICATION/account_service/account_5482/VERIFY_RECORD_ABSENT ev="evidence_92"<br>MISSING  #4 VERIFICATION/analytics_platform/analytics_6077/VERIFY_RECORD_ANONYMIZED ev="evidence_92"<br>EXTRA    #1 VERIFICATION/account_service/account_7012/VERIFY_RECORD_ABSENT ev="evidence_92"<br>EXTRA    #4 VERIFICATION/analytics_platform/analytics_4574/VERIFY_RECORD_ANONYMIZED ev="evidence_92" |
| case_29 | INCOMPLETE / INCOMPLETE | MISSING  #9 VERIFICATION/account_service/account_7012/VERIFY_RECORD_ABSENT ev=""<br>EXTRA    #9 VERIFICATION/account_service/account_5482/VERIFY_RECORD_ABSENT ev="" |

## 2. The full 518-request run

There is no answer key for these, so the checks are structural and consistency checks:

| Check | Result |
|---|---|
| Rows | 518 (one per request, sorted by `case_id`) |
| Status distribution | COMPLETE 169, INCOMPLETE 339, NOT_APPLICABLE 10 |
| Total steps emitted | 3053 |
| Submission-contract validator (`cmd/validate`) | **pass**: header, row count/order, status values, JSON shape, consecutive step numbers, allowed operation/phase/state pairs, phase order, empty `evidence_id` on future work, evidence cited for findings and COMPLETE verifications, and **no cited evidence captured after `audit_at`** |
| Validator warning | case_61: INCOMPLETE with no steps (nothing identifiable at audit time; completion unproven) |

## 3. Intent classification (NOT_APPLICABLE)

A request that asks for access, correction, export, a marketing opt-out or support, or that explicitly says the account must be kept, is `NOT_APPLICABLE` with `steps_json=[]`. A follow-up asking to *verify* a deletion is still a deletion case (the samples confirm this).

- Model: Gemini with a structured-output schema; the full prompt is `internal/intent/intent.go` (`Prompt`). Identical texts are classified once; results are cached.
- Verdict sources in the final run: 398 live Gemini calls, 9 served from the content-hash cache (identical request texts recur and are classified once). Request types returned: ACCESS 3, CORRECTION 3, DELETE_ACCOUNT 491, EXPORT 1, MARKETING_OPT_OUT 2, SUPPORT 1, VERIFY_DELETION 17.
- **Independent check:** the global activity stream contains 10 `CLASSIFY_REQUEST` events (privacy intake logging "classified as non-deletion"). The classifier found exactly **10** non-deletion requests, `case_533`..`case_542`, matching that count; all ten are access / export / correction / marketing / support requests with explicit "keep my account" wording.
- Offline fallback (`Heuristic`, multilingual keywords + negation): also flags exactly those ten once the French "suppression" pattern was fixed (an earlier version wrongly flagged 12 French requests; found by comparing the fallback with the model).
- If an intent call fails, the run **aborts** instead of silently falling back (see `--allow-degraded`), so a blocked API key can never produce a weaker `output.csv` unnoticed.

## 4. Additional evaluation cases created for this project

These are executable (`go test -v ./internal/audit/`), each targets a failure mode the dataset is designed to provoke, and all pass on the final database.

| # | Custom case | What it proves | Result |
|---|---|---|---|
| 1 | **Time travel.** Take every public sample and move `audit_at` back to 2026-07-01, before most evidence exists. | No step cites evidence captured after the cutoff; later evidence cannot change an earlier audit. (70 citations checked.) | pass |
| 2 | **Historical identifier.** For 5 requests made with an *old* email, re-run the same request with the user's *current* email. | Both resolve to the same user and produce a byte-identical audit; the identity graph's validity windows work. | pass |
| 3 | **Determinism.** Run every sample twice. | Identical status and `steps_json` bytes every time (no map-order or concurrency nondeterminism). | pass |
| 4 | **Contract.** Validate the sample predictions with the same validator used for `output.csv`. | The output format rules hold on a set where a gold key exists. | pass |
| 5 | **Non-deletion intent, offline.** Nine hand-written messages: marketing-only, correction, copy-of-invoices, explicit negations, and deletion requests in Spanish, French, Portuguese, Japanese and English. | Negation always wins; deletion wording is caught in other languages. | pass |
| 6 | **Natural experiment.** The ten real non-deletion requests in the evaluation set vs. the ten `CLASSIFY_REQUEST` events. | The LLM classifier agrees with the intake system on every one. | match |

## 5. Failures found and improvements made

Each row is a measured step on the 32 public samples. "Precision" is step-level precision.

| Iteration | Status acc. | Precision | Recall | Exact sequences | What was wrong -> what changed |
|---|---:|---:|---:|---:|---|
| v0 baseline | 90.6% | 51.3% | 90.7% | 3.1% | Every deletion-worker event in the request window was treated as the user's resource: ~166 false steps. |
| + worker-activity attribution | 96.9% | 94.2% | 92.2% | 50.0% | Resources with no identifier (already-deleted accounts, anonymized profiles) are now tied to a case only through worker activity near `reported_at`; the activity result must agree with the case's `reported_status`; matching is global (each resource belongs to one case). Category now comes from the record's own `record_type`, structural hints and the worker's action type (an analytics-style id was really a marketing profile; a support ticket can be an attachment). |
| + relevance and citation fixes | 100.0% | 93.8% | 93.3% | 62.5% | Evidence-linked resources must appear in evidence captured at/after the request (an old 404 lookup from March is not this account); never-observed REDACT/ANONYMIZE records are skipped; records proven absent by omission cite the case's own primary structured evidence file; resource ids written inside document text (`CS-4827 / support_001`) are indexed. |

Other failures found and fixed along the way:

- **Silent degradation.** After a Gemini key was blocked (HTTP 403), a run produced a valid-looking but weaker output (status accuracy fell to 87.5% and recall to 87.0% on the samples because the 15 image/PDF files were not read). This doubles as an **ablation: vision evidence is worth ~12 points of status accuracy and ~6 points of recall**. Fix: strict mode (LLM failures abort the run), a cache backup before every run, and a validator that must pass before submitting.
- **Validator-found bug.** The contract validator flagged four COMPLETE cases with no steps. Three were the missing vision data above; `case_61` is a genuine time-travel case (same person as sample `case_07`, audited months earlier, before any of their later evidence existed). Policy: COMPLETE requires every relevant copy to be *proven* compliant, so when nothing can be identified at audit time the status is INCOMPLETE (reported as a validator warning, not an error).
- **Re-ingest on Windows.** DBeaver holding `audit.db` open made the rebuild fail silently; the rebuild now clears the schema in place.
- **Regex bug.** A Python-generated Go regex lost its `\\b` word boundaries and indexed region names (`eu_west_1`) as resources; caught by the resource count jumping 4,468 -> 4,778, fixed and restricted to records that already carry a non-conforming id.

## 6. Known limitations (honest failure analysis)

- **Retained audit records that exist nowhere in the data.** Seven sample answers include a `VERIFY_RECORD_PRESENT` step for an `audit_record_*` id (e.g. `audit_record_6645`) that appears in no supplied file, so it cannot be reconstructed. Each such case loses exactly one verification step.
- **Cases that share a `reported_at`.** Two or three cases can compete for the same unidentified account or analytics profile and the data does not separate them; the global matching picks deterministically but can swap them (samples 03, 23, 24, 26-29). On the samples accounts attribute correctly ~12/18 and analytics profiles ~4/7 times; the identified-by-evidence resources are essentially all correct.
- **Offsets were fitted on observed data.** Attribution windows/centres are configured in `internal/audit/rules.go`; the analytics centre is learned from resources already linked by identifiers. A dataset with a different workflow cadence would need those re-checked (the evaluator makes that a one-command test).
- **Cost figures are estimates** (see `usage_report.md`).
