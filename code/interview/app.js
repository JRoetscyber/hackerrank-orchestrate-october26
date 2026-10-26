/* Interview companion: flow diagram + reasoning. No dependencies. All figures come from the final run. */
'use strict';

/* ------------------------------------------------------------------ diagram */
const W = 130, H = 74;
const COL = [20, 205, 390, 575, 760, 945, 1100];
const ROW = [40, 215, 385];

const NODES = {
  in_csv:  { x: COL[0], y: ROW[0], w: W, h: H, kind: 'data', t: 'CSV inputs', s: 'requests · rules · activity (8)' },
  in_evid: { x: COL[0], y: ROW[1], w: W, h: H, kind: 'data', t: 'Evidence files', s: 'SQL · JSON · log · txt (79)' },
  in_img:  { x: COL[0], y: ROW[2], w: W, h: H, kind: 'data', t: 'Images & PDFs', s: '11 PNG + 4 PDF' },
  s0:      { x: COL[1], y: ROW[0], w: W, h: H, kind: 'det',  t: '0 · Ingest', s: 'Go loaders → SQLite' },
  vision:  { x: COL[1], y: ROW[2], w: W, h: H, kind: 'llm',  t: 'Gemini vision', s: 'pixels → records' },
  db:      { x: COL[2], y: ROW[0], w: W, h: H, kind: 'data', t: 'SQLite audit.db', s: 'relational layer' },
  s1:      { x: COL[2], y: ROW[1], w: W, h: H, kind: 'llm',  t: '1 · Intent', s: 'deletion or not?' },
  cache:   { x: COL[2], y: ROW[2], w: W, h: H, kind: 'data', t: 'cache.db', s: 'LLM results + usage' },
  s2:      { x: COL[3], y: ROW[0], w: W, h: H, kind: 'det',  t: '2 · Identity graph', s: 'current + historical' },
  na:      { x: COL[3], y: ROW[1], w: W, h: H, kind: 'out',  t: 'NOT_APPLICABLE', s: 'steps_json = []' },
  s3:      { x: COL[4], y: ROW[0], w: W, h: H, kind: 'det',  t: '3 · Evidence & state', s: 'cut at audit_at' },
  s4:      { x: COL[5], y: ROW[0], w: W, h: H, kind: 'det',  t: '4 · Rules → steps', s: 'order + citations' },
  s5:      { x: COL[5], y: ROW[1], w: W, h: H, kind: 'det',  t: '5 · Validate', s: 'contract check' },
  out:     { x: COL[6], y: ROW[0], w: 115, h: H, kind: 'out', t: 'output.csv', s: '518 rows' },
  eval:    { x: COL[5], y: ROW[2], w: W, h: H, kind: 'det',  t: 'Eval & reports', s: 'samples · tests · usage' },
};

const EDGES = [
  ['in_csv', 's0', ''], ['in_evid', 's0', 'parse'], ['in_img', 'vision', ''],
  ['vision', 's0', 'records', 'llm'], ['s0', 'db', 'tables'], ['db', 's2', 'queries'],
  ['db', 's1', 'request_text'], ['s1', 's2', 'authorizes deletion'], ['s1', 'na', 'no'],
  ['s2', 's3', 'ids'], ['s3', 's4', 'states'], ['s4', 's5', 'steps'],
  ['s5', 'out', 'sorted rows'], ['s5', 'eval', ''],
  ['vision', 'cache', 'cached', 'llm dash'], ['s1', 'cache', 'cached', 'llm dash'],
];

/* One request travelling through the system (centres of the main-path nodes). */
const MAIN = ['in_csv', 's0', 'db', 's1', 's2', 's3', 's4', 's5', 'out'];

const D = {
  in_csv: {
    tag: 'Input · data', title: 'Request and context CSVs',
    what: 'deletion_requests (518), sample_deletion_requests (32), sample_output (32 correct answers), user_identities (850 users), user_audit_log (306 identity changes), system_activity (4,011 events), deletion_rules (15) and the evidence manifest (94 files).',
    how: 'A dynamic loader creates one table per CSV straight from its header. A first column that looks like an id and is unique becomes the primary key; id, email, phone, hash and time-like columns are indexed automatically. steps_json in any answer file is unnested into a relational sample_output_steps table so expected answers can be joined and diffed in SQL.',
    why: 'The brief says data can change. Nothing about the CSV shapes is hard-coded, so a new file or an extra column needs no code change.',
    code: 'code/internal/store/csvload.go',
    fail: 'UTF-8 BOM and ragged rows are handled; the blank dataset/output.csv template is skipped on purpose.'
  },
  in_evid: {
    tag: 'Input · data', title: 'Text evidence: SQL, JSON, logs, text (79 files)',
    what: '24 SQL snapshot exports (primary and replica tables), 28 JSON files (snapshot bundles, service exports, ledgers), 26 logs (logfmt and JSON-lines in several dialects) and 1 pipe-delimited handover record.',
    how: 'SQL files are executed in a throw-away in-memory SQLite and every table is read back by enumerating sqlite_master, so no table or column names are assumed. JSON is walked generically: any object with a resource_id is a record and the surrounding keys supply system, collection and time. Logs become one record per line. Every record is flattened into leaf fields, each classified as email, hash, phone, user id, resource id, name or text, and emails/hashes/user ids buried in free text are extracted too.',
    why: 'Deterministic parsing cannot hallucinate. It also means every evidence id in the answer was found by a query.',
    code: 'code/internal/evidence/extractors.go, flatten.go',
    fail: 'Unknown file types are registered with status "skipped" rather than ignored; manifest rows with no file on disk are recorded as "missing".'
  },
  in_img: {
    tag: 'Input · data', title: 'Images and PDFs (15 files)',
    what: 'Application screenshots (admin consoles, backup and connector consoles, search inspector, audit explorer) and PDFs (invoices, retention registers, a delivery-correction attachment). They are ordinary business artifacts, not verdicts.',
    how: 'Sent to the vision step below. The text pipeline cannot read them, but the evidence is real: for example the admin console screenshot is the only place that ties a customer to both user_11 and account_5762.',
    why: 'Ablation, measured by accident: with these 15 files missing, sample status accuracy fell from 100% to 87.5% and step recall from 93.3% to 87.0%.',
    code: 'dataset/evidence/images, dataset/evidence/documents',
    fail: 'If the model is unavailable the run stops (strict mode) instead of quietly producing a weaker answer.'
  },
  s0: {
    tag: 'Deterministic · Go', title: 'Stage 0: ingest into SQLite',
    what: 'Rebuilds data/audit.db from dataset/ on every run.',
    how: 'Create schema → load CSVs → build the identity graph → walk dataset/evidence (not only the manifest) → extract records → link evidence to users → build resource links. Text evidence is written in one transaction; vision runs outside any transaction so each paid response is cached the instant it arrives; vision records are then written in a second transaction. If Windows will not let the old database file be deleted (DBeaver holds it open) the schema is cleared in place instead.',
    why: 'A disposable database rebuilt from source means the DB can never drift from the data, and "dynamic" is simply "run it again".',
    code: 'code/internal/pipeline/pipeline.go, code/internal/evidence/ingest.go',
    numbers: '94 files, 4,210 records, 34,061 fields. About 1.5 to 2.5 s without vision; about 23 s including 15 live vision calls. The full 518-case audit then takes about 1.4 s.',
    fail: 'Any extraction error aborts the run unless --allow-degraded is set.'
  },
  vision: {
    tag: 'LLM · Gemini', title: 'Vision extraction (images and PDFs)',
    what: 'Turns each screenshot or PDF into records in the same shape as every other evidence record.',
    how: 'One structured-output call per file at temperature 0: document type, system, visible text, and one record per table row or detail panel with labelled fields. The prompt lists the system ids actually present in the data, so it adapts to new systems. Responses are cached by file hash + model + prompt version.',
    why: 'Pixels need a model; plain OCR would lose rows, panels and the link between a label and its value. The model only transcribes; it never decides anything about deletion.',
    code: 'code/internal/evidence/vision.go',
    numbers: '15 live calls, 49,085 tokens, about $0.07 estimated, about 5 s per call. One file was checked against the screenshot by eye: every row matched.',
    fail: 'HTTP 403 (project denied) is what triggered the strict-mode redesign; see the incident section.'
  },
  db: {
    tag: 'Data · SQLite', title: 'The relational layer',
    what: 'Raw CSV tables plus a curated model: users, identifiers (with validity windows), evidence_documents, evidence_snapshots, evidence_records, evidence_fields, record_identifiers, resource_links, resources, systems; views request_subjects and resource_observations.',
    how: 'Foreign keys are on and PRAGMA foreign_key_check is clean. The time rule is literally a WHERE clause (captured_at <= audit_at). The same database opens in DBeaver for manual inspection.',
    why: 'The task is a graph of identities, resources and evidence across time. Joins and indexes express that directly; hand-written loops over CSVs would not.',
    code: 'code/internal/store/schema.go',
    numbers: '850 users, 4,556 identifiers, 3,642 direct + 717 trace record-to-user links, 36 systems, ~4,470 resources.'
  },
  s1: {
    tag: 'LLM · Gemini', title: 'Stage 1: does this request authorize deletion?',
    what: 'The only judgement on free text. Decides DELETE vs NOT_APPLICABLE before any evidence work starts.',
    how: 'A structured-output prompt with explicit rules: erase/close/remove in any language is a deletion; asking to verify or audit a deletion is still a deletion case (the samples prove it); access, export, correction, marketing opt-out and support are not; an explicit "keep my account" always wins. Identical texts are classified once and cached. A multilingual keyword fallback exists for offline use.',
    why: 'Requests come in Spanish, French, Portuguese, Japanese and English, and negation ("I am not asking for deletion") defeats keyword rules. This is the right use of a language model.',
    code: 'code/internal/intent/intent.go (the full prompt is in the source)',
    numbers: '10 requests flagged NOT_APPLICABLE (case_533 to case_542), exactly matching the 10 CLASSIFY_REQUEST events in the activity stream. 407 distinct texts, 398 live calls, about 1.1 s each.',
    fail: 'If a call fails the run stops. An earlier fallback version wrongly flagged 12 French requests ("suppression"); it was caught by comparing it with the model and the regex was fixed.'
  },
  cache: {
    tag: 'Data · SQLite', title: 'cache.db: the memory of every paid call',
    what: 'vision_cache, intent_cache and llm_usage, attached to the main database as "cache".',
    how: 'Cache keys combine the content hash, the model name and a hash of the prompt, so changing either invalidates results automatically. Every call, live or cached, is a row in llm_usage with tokens, latency and cost; the usage report is a query over it. The file is backed up before each run.',
    why: 'Reruns are free, results are reproducible, and the usage report is exact rather than estimated by hand.',
    code: 'code/internal/store/store.go',
    fail: 'It is never deleted casually: deleting it once cost the 15 vision results (see the incident).'
  },
  s2: {
    tag: 'Deterministic · SQL', title: 'Stage 2: identity graph',
    what: 'Maps the request email, which may be old, to a stable user and every identifier that user has ever held.',
    how: 'The audit log is replayed in time order into validity windows (valid_from, valid_to) per field; the current snapshot closes or extends them. SHA-256 email hashes are derived for every email window so a hash found in evidence still links even if the audit log never recorded it.',
    why: 'Old systems and old logs contain old identifiers. Windows also answer "did the user hold this email when the record was written?".',
    code: 'code/internal/store/identity.go',
    numbers: '518 of 518 requests resolve to exactly one user: 418 by current email, 100 by a historical email. Custom test: an old-email request and a current-email request give byte-identical audits.'
  },
  s3: {
    tag: 'Deterministic · Go + SQL', title: 'Stage 3: find the user\'s resources and their state at audit_at',
    what: 'Which records belong to this user, and what is the true state of each one at the audit cut-off.',
    how: '(a) Resources linked by identifiers, email, phone, hash, user id, or by shared trace id, but only if they appear in evidence captured at or after the request. (b) Resources with no identifier at all (an already-deleted account, an anonymized profile) are tied to a case by deletion-worker activity near reported_at, where the worker result must agree with the case reported_status, matched globally so each resource belongs to one case. (c) Evidence is eligible only if the file was captured at or before audit_at; a resource\'s state comes from the latest eligible file that observes it, taking the union inside that file so a record that is gone from the primary but present on a replica counts as present. (d) A fact extractor normalizes about a dozen record shapes into exists / identifiers exposed / enabled / restorable.',
    why: 'Workflow statuses and activity results are claims, not proof; they are used only to attribute, never to decide state.',
    code: 'code/internal/audit/engine.go, facts.go, attribution.go',
    fail: 'Cases that share a reported_at can compete for the same unidentified resource; the data does not separate them (see limits).'
  },
  s4: {
    tag: 'Deterministic · Go', title: 'Stage 4: apply the rules and write the steps',
    what: 'deletion_rules.csv decides the required treatment per data category; the engine compares it with the observed state.',
    how: 'DELETE: present is a violation, verify absent. REDACT / ANONYMIZE: exposed identifiers are a violation, verify redacted / anonymized. RETAIN: audit records and legal holds must still exist. Data flows are disabled and risky restores blocked before dependent records change. Order inside each phase is fixed, ties go by system then resource. Each finding and each COMPLETE verification cites the latest eligible evidence file that observed the resource; future work cites nothing.',
    why: 'The grader compares exact operations and order. Deterministic code is reproducible and cannot invent an evidence id; the LLM adds nothing here but risk.',
    code: 'code/internal/audit/plan.go, rules.go',
    fail: 'COMPLETE needs proof. If nothing can be identified at audit time the case is INCOMPLETE with a note (case_61).'
  },
  s5: {
    tag: 'Deterministic · Go', title: 'Stage 5: validate and export',
    what: 'A contract validator that must pass before anything is submitted.',
    how: 'Checks: exact header; 518 rows, one per case, sorted; status values; valid JSON array; consecutive step numbers; allowed operation/phase/state pairs; phase order; empty evidence_id on future work; evidence on findings and COMPLETE verifications; no evidence captured after audit_at; NOT_APPLICABLE has [].',
    why: 'It found a real bug during development (four COMPLETE cases with no steps), so it earns its place.',
    code: 'code/internal/validate/validate.go, cmd/validate',
    numbers: 'Passes on output.csv. One deliberate warning: case_61.'
  },
  na: {
    tag: 'Exit · output', title: 'NOT_APPLICABLE',
    what: 'Requests that do not authorize deletion exit here with steps_json = [].',
    how: 'Examples: "Please stop the promotional emails. I still need the account", "I\'d like to download my account information, keep the account as it is", "My surname is misspelled; correct it without removing any of my records".',
    why: 'The brief says an access, correction, export or opt-out request is NOT_APPLICABLE even if every other field looks like a deletion case.'
  },
  out: {
    tag: 'Output', title: 'output.csv',
    what: '518 rows: case_id, deletion_status, steps_json, sorted by case_id.',
    how: '169 COMPLETE, 339 INCOMPLETE, 10 NOT_APPLICABLE, 3,053 steps in total. No step cites evidence captured after its case\'s audit_at.',
    why: 'This is the submission artifact, produced by one command from the dataset.'
  },
  eval: {
    tag: 'Deterministic · Go', title: 'Evaluation and reports',
    what: 'cmd/eval scores the 32 public samples; cmd/report writes evaluation_report.md and usage_report.md; go test runs the custom evaluation cases.',
    how: 'Metrics: status accuracy, step precision/recall/F1, field accuracy (state, evidence_id, step_no), exact-step rate, exact-sequence rate and a sequence similarity (LCS ratio). Six custom cases: time travel, historical identifier, determinism, contract, offline intent, and a natural experiment against the CLASSIFY_REQUEST events.',
    why: 'Every claim in the reports is regenerated from the run, not typed by hand.',
    code: 'code/cmd/eval, code/cmd/report, code/internal/score, code/internal/audit/custom_test.go',
    numbers: '100% status (32/32), 93.8% precision, 93.3% recall, 98.7% field accuracy, 62.5% identical sequences.'
  },
};

const svg = document.getElementById('svg');
const NS = 'http://www.w3.org/2000/svg';
const el = (n, a = {}, parent) => { const e = document.createElementNS(NS, n); for (const k in a) e.setAttribute(k, a[k]); if (parent) parent.appendChild(e); return e; };
const ctr = (n) => [n.x + n.w / 2, n.y + n.h / 2];

function anchors(a, b) {
  const [ax, ay] = ctr(a), [bx, by] = ctr(b);
  const dx = bx - ax, dy = by - ay;
  if (Math.abs(dy) < 10) return dx > 0 ? [[a.x + a.w, ay], [b.x, by]] : [[a.x, ay], [b.x + b.w, by]];
  if (Math.abs(dx) < 10) return dy > 0 ? [[ax, a.y + a.h], [bx, b.y]] : [[ax, a.y], [bx, b.y + b.h]];
  if (Math.abs(dx) > Math.abs(dy)) return dx > 0 ? [[a.x + a.w, ay], [b.x, by]] : [[a.x, ay], [b.x + b.w, by]];
  return dy > 0 ? [[ax, a.y + a.h], [bx, b.y]] : [[ax, a.y], [bx, b.y + b.h]];
}

function drawDiagram() {
  const defs = el('defs', {}, svg);
  ['', 'llm'].forEach((c) => {
    const m = el('marker', { id: 'arrow' + c, viewBox: '0 0 10 10', refX: 9, refY: 5, markerWidth: 7, markerHeight: 7, orient: 'auto-start-reverse' }, defs);
    el('path', { d: 'M0 0L10 5L0 10z', fill: c ? 'var(--llm)' : 'var(--muted)' }, m);
  });
  const edgeLayer = el('g', {}, svg), dotLayer = el('g', {}, svg), nodeLayer = el('g', {}, svg);
  EDGES.forEach(([f, t, label, cls = '']) => {
    const [[x1, y1], [x2, y2]] = anchors(NODES[f], NODES[t]);
    const horiz = Math.abs(y1 - y2) < 10 || Math.abs(x2 - x1) > Math.abs(y2 - y1);
    const c1 = horiz ? [x1 + (x2 - x1) / 2, y1] : [x1, y1 + (y2 - y1) / 2];
    const c2 = horiz ? [x1 + (x2 - x1) / 2, y2] : [x2, y1 + (y2 - y1) / 2];
    const e = el('path', { d: `M${x1} ${y1} C${c1[0]} ${c1[1]} ${c2[0]} ${c2[1]} ${x2} ${y2}`, class: 'edge ' + cls, 'marker-end': `url(#arrow${cls.includes('llm') ? 'llm' : ''})` }, edgeLayer);
    if (label) {
      const t2 = el('text', { x: (x1 + x2) / 2 + (horiz ? 0 : 8), y: (y1 + y2) / 2 + (horiz ? -7 : 4), class: 'edgelabel', 'text-anchor': horiz ? 'middle' : 'start' }, edgeLayer);
      t2.textContent = label;
    }
  });
  Object.entries(NODES).forEach(([id, n]) => {
    const g = el('g', { class: 'node ' + n.kind, tabindex: 0, role: 'button', 'aria-label': n.t, 'data-id': id }, nodeLayer);
    el('rect', { x: n.x, y: n.y, width: n.w, height: n.h }, g);
    const t = el('text', { x: n.x + n.w / 2, y: n.y + 30, 'text-anchor': 'middle' }, g); t.textContent = n.t;
    const s = el('text', { x: n.x + n.w / 2, y: n.y + 49, 'text-anchor': 'middle', class: 's' }, g); s.textContent = n.s;
    g.addEventListener('click', () => select(id));
    g.addEventListener('keydown', (ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); select(id); } });
  });
  const pts = MAIN.map((k) => ctr(NODES[k]));
  const path = 'M' + pts.map((p) => p.join(' ')).join(' L');
  [0, -3.3, -6.6].forEach((begin) => {
    const c = el('circle', { r: 6, class: 'dot' }, dotLayer);
    const m = el('animateMotion', { dur: '10s', repeatCount: 'indefinite', begin: begin + 's', path }, c);
    m.setAttribute('rotate', 'auto');
  });
}

function select(id) {
  document.querySelectorAll('.node').forEach((n) => n.classList.toggle('sel', n.dataset.id === id));
  const d = D[id], n = NODES[id];
  const box = document.getElementById('detail');
  box.className = 'detail ' + n.kind;
  const row = (k, v) => v ? `<dt>${k}</dt><dd>${v}</dd>` : '';
  box.innerHTML = `<span class="tag">${d.tag}</span><h3>${d.title}</h3><dl>
    ${row('What', d.what)}${row('How', d.how)}${row('Why', d.why)}${row('Where in code', d.code ? `<code>${d.code}</code>` : '')}
    ${row('Measured', d.numbers)}${row('If it goes wrong', d.fail)}</dl>`;
}

document.getElementById('anim').addEventListener('change', (e) => { e.target.checked ? svg.unpauseAnimations() : svg.pauseAnimations(); });

/* ------------------------------------------------------------------ numbers */
const NUMBERS = [
  ['100%', 'status accuracy, 32/32 public samples'],
  ['93.8% / 93.3%', 'step precision / recall'],
  ['98.7%', 'field accuracy on matched steps'],
  ['518 / 518', 'rows, contract validator passes'],
  ['10 = 10', 'non-deletion requests found vs CLASSIFY_REQUEST events'],
  ['0', 'steps citing evidence from after audit_at'],
  ['94 files', '4,210 records, 34,061 fields ingested'],
  ['850 users', '4,556 identifiers; 100 requests via an old email'],
  ['422 calls', '283,394 tokens in the final run'],
  ['about $0.19', 'estimated total; $0.00036 per request'],
  ['547 tokens', 'average per request'],
  ['2 s + 1.4 s', 'ingest (no vision) + full 518-case audit'],
];
document.getElementById('numgrid').innerHTML = NUMBERS.map(([a, b]) => `<div class="stat"><b>${a}</b><span>${b}</span></div>`).join('');

/* ------------------------------------------------------------------ choices */
const CHOICES = [
  ['Deterministic first, LLM second', 'Go and SQL do the work; Gemini only reads pixels and language.',
    { Alternatives: 'Give a large model the whole case and ask for steps_json (the first blueprint draft did this for synthesis).', Why: 'The grader compares exact operations, order and evidence ids. Rules in code are reproducible, cheap, testable and cannot invent an id. A model is used only where code cannot: images and multilingual intent.', Cost: 'More engineering up front; unusual record shapes need a rule instead of "just ask the model".' }],
  ['A relational SQLite database', 'One rebuilt file with an identity graph, evidence records and links.',
    { Alternatives: 'In-memory maps, a vector store / RAG, Postgres.', Why: 'The problem is joins across identities, resources and time. SQL expresses "evidence for this user captured before this cut-off" in one query, is inspectable in DBeaver, and a disposable file rebuilt from source cannot drift.', Cost: 'A schema to design; single-writer constraints (handled with one connection and a single consumer goroutine).' }],
  ['Dynamic everywhere', 'Tables from headers, evidence found by walking the folder, SQL read by enumerating tables, JSON walked generically.',
    { Alternatives: 'Hard-code the known 8 CSVs, 94 files and ~14 JSON shapes.', Why: 'You were told data may change. The only fixed knowledge is a small documented configuration (internal/audit/rules.go).', Cost: 'Generic code is a little more abstract and needs the evaluator to prove it still works.' }],
  ['Vision LLM only for pixels, cached by content hash', 'Gemini transcribes screenshots and PDFs into the same record shape.',
    { Alternatives: 'Plain OCR; manual transcription; ignoring images.', Why: 'OCR loses rows, panels and label-value pairing; ignoring images measurably costs about 12 points of status accuracy. The model never judges deletion, so a transcription error is visible in the database.', Cost: '15 calls, about $0.07; needs an API that works (see the incident).' }],
  ['Intent by LLM with explicit negation rules, plus a fallback', 'Structured-output classifier with a multilingual keyword backup.',
    { Alternatives: 'Keywords only; assume everything is a deletion.', Why: 'Negation and verification requests defeat keywords. Result: exactly the 10 requests the intake system also flagged. Samples show "verify my deletion" is still audited.', Cost: '398 live calls; the fallback must never silently replace the model (strict mode).' }],
  ['Evidence is cut by file capture time, citation is the latest eligible file', 'Only files captured at or before audit_at count.',
    { Alternatives: 'Cut by each record\'s own timestamp; cite any matching file.', Why: 'A bundle exported on 09-17 can contain a record stamped 09-15. The answer key cites the file that existed at audit time, and this rule reproduces 70 of 75 sample citations.', Cost: 'Needs the manifest capture times to be trustworthy.' }],
  ['State = latest eligible file, union inside it', 'Replica drift is detected inside one snapshot.',
    { Alternatives: 'Trust the primary table; trust the most recent record anywhere.', Why: 'A record deleted on the primary but still on a failover replica is still there. Comparing tables in the same snapshot catches it.', Cost: 'Needs per-record fact extraction across about a dozen shapes.' }],
  ['Attribute identifier-less resources through worker activity', 'Match an unidentified account or analytics profile to a case by activity near reported_at.',
    { Alternatives: 'Ignore them; guess by id.', Why: 'The task lists timestamps as a grounded link and the worker result matches the case reported status. Matching is global (each resource belongs to one case) and deterministic.', Cost: 'Cases sharing a reported_at can be swapped; roughly 12 of 18 accounts and 4 of 7 analytics profiles attribute correctly on the samples. Stated openly.' }],
  ['Category from record_type, structure hints, then worker action', 'Decides whether a support ticket is a message or attachment, an analytics-style id a marketing profile.',
    { Alternatives: 'Map by id prefix only.', Why: 'A prefix is ambiguous: analytics_9914 was a marketing profile (delete), support_002 an attachment (delete). The record states its own type most of the time.', Cost: 'A hint table to maintain.' }],
  ['Rules and schema live in Go source', 'Not in .json or .sql files.',
    { Alternatives: 'External config and schema files.', Why: 'The code judge ignores .csv/.json/.sql/.log files. Keeping the data model and rules in Go means reviewers actually see them.', Cost: 'None really; the JSON is still parsed from a constant so it stays editable.' }],
  ['Ordering derived from the samples', 'A fixed operation order per phase, ties by system then resource.',
    { Alternatives: 'Order by discovery.', Why: 'Derived from all 32 sample answers with zero contradictions; findings and remediation also obey the stated dependency rule (stop flows and restores first).', Cost: 'Verification order for rare combinations was inferred, not proven.' }],
  ['COMPLETE requires proof', 'When nothing identifiable exists at audit time the case is INCOMPLETE.',
    { Alternatives: 'Default to COMPLETE when nothing is found.', Why: 'The contract defines COMPLETE as every relevant copy proven compliant. Absence of evidence is not evidence of completion (case_61, a time-travel case).', Cost: 'Possible false INCOMPLETE; surfaced as a validator warning.' }],
  ['Strict mode, cache backups, a validator', 'A failed LLM call aborts the run; the cache is backed up first; the contract is checked before submitting.',
    { Alternatives: 'Fall back quietly and carry on.', Why: 'Born from the incident: a quiet fallback produced a plausible but weaker output.csv. A silent degrade is worse than a loud failure.', Cost: 'You must fix the API before you can finish a run, by design.' }],
  ['A small, cheap model (Gemini flash-lite)', 'Chosen for speed, cost and context length.',
    { Alternatives: 'A larger model for everything.', Why: 'Both jobs are narrow and schema-constrained. About 547 tokens per request, about $0.19 for the whole dataset.', Cost: 'Less headroom on very hard images; mitigated by structured output and review of the extraction.' }],
];
document.getElementById('choicelist').innerHTML = CHOICES.map(([t, sub, d], i) => card(i + 1, t, sub, d)).join('');

function card(n, title, sub, dl) {
  const body = Object.entries(dl).map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('');
  return `<div class="card"><button aria-expanded="false"><span class="n">${n}</span><span class="t">${title}<small>${sub}</small></span><span class="chev">&#9656;</span></button><div class="body"><dl>${body}</dl></div></div>`;
}

/* ------------------------------------------------------------------ stack */
const STACK = [
  { h: 'Go', said: 'Your words, tidied: Go is the main language for its hardware footprint and near-compiler speed.',
    facts: ['The deterministic part of the pipeline is fast: ingesting 4,000+ records takes about 2 seconds and the full 518-case audit about 1.4 seconds (measured); the rest of a run is waiting on the model.',
      'A pure-Go SQLite driver means no C compiler and no runtime to install: one go run works on Windows, macOS and Linux, and the program is a single static binary.',
      'Only one direct third-party dependency (modernc.org/sqlite). CSV, JSON, HTTP, hashing, regex and concurrency are all standard library.',
      'Goroutines with a semaphore run the 15 vision calls and ~400 intent calls concurrently, with a single consumer goroutine owning the one-connection database, so there are no data races and the output is deterministic.',
      'A compiled, statically typed language catches shape mistakes (the step struct, the allowed operation table) at build time, which matters when the grader is strict about JSON.'],
    caveat: 'Honest trade-off: Python would have been faster to prototype and the vision SDK is more polished. Go costs a little speed of iteration for a lot of speed and reliability at run time.' },
  { h: 'SQL (SQLite)', said: 'Your words, tidied: SQLite is the database of choice for its ease of use.',
    facts: ['One file, no server, nothing to configure: anyone can clone the repo, run one command, and open data/audit.db in DBeaver (you did).',
      'The domain is relational: identities, identifier history with validity windows, resources, evidence records and the links between them. The cut-off rule is a WHERE clause and the identity lookup is a join.',
      'Integrity is checkable: foreign keys are on and PRAGMA foreign_key_check returns nothing.',
      'ATTACH lets the persistent cache live in a second file beside a disposable audit database; WAL mode keeps reads and writes cheap.',
      'SQL snapshot evidence is itself SQLite-compatible, so the same engine reads the evidence and holds the model.'],
    caveat: 'Honest trade-off: SQLite is single-writer. That fits a batch audit and is handled with one connection; Postgres would only be needed for concurrent services.' },
  { h: 'The SDLC', said: 'From your blueprint: "Deterministic Go first, AI second." Go and SQLite do the bulk of the work; the LLM only handles what needs fuzzy, human-like judgement.',
    facts: ['The blueprint fixed the philosophy and the stages before any code existed, which is why the finished system has the same shape.',
      'Where reality disagreed with the plan, the plan changed in the open: column names differed, the LLM was removed from step synthesis, reports moved under code/.',
      'Testing was a loop, not a phase: a baseline of 51% precision became 94% by measuring against the 32 samples after every change.',
      'Operations lessons became features: strict mode, cache backups, a contract validator.',
      'Every conversation turn is logged in log.txt as the rules require.'],
    caveat: 'Section 5 shows plan versus reality side by side, which is the honest way to present an SDLC.' },
];
document.getElementById('stackgrid').innerHTML = STACK.map((p) => `<div class="pillar"><h3>${p.h}</h3>
  <div class="said">${p.said}</div><h4>What backs it up in this build</h4><ul>${p.facts.map((f) => `<li>${f}</li>`).join('')}</ul>
  <h4>Say this if challenged</h4><p>${p.caveat}</p></div>`).join('');

/* ------------------------------------------------------------------ SDLC */
const SDLC = [
  ['1. Requirements and analysis', 'Read the inputs; understand the audit contract; list the 8 input files.', 'Found that the blueprint column names did not match the data (user_identifier vs email, evidence_id vs file_id, timestamp vs event_time) and that system_activity has no user id, so activity links to users only through resource ids. The samples contain no NOT_APPLICABLE case.', 'Verify assumptions against the data on day one.'],
  ['2. Architecture', 'Six stages: SQLite ingest, intent, identity, evidence hunt, LLM synthesis of steps_json, validation.', 'Kept five of six. The LLM no longer writes steps_json: the grader needs exact operations and order, and the rules are deterministic. Gemini is used for vision and intent only.', 'Use a model where code cannot, not where code is better.'],
  ['3. Detailed design', 'Tables per CSV, FTS5 search over evidence, per-stage packages.', 'A dynamic loader and a curated relational schema instead of FTS5 text search (resource and identifier joins did the job). Schema and rules moved into Go source because the code judge ignores .sql/.json files.', 'Design for the reviewer as well as the machine.'],
  ['4. Implementation', 'A five-hour hour-by-hour roadmap.', 'Built incrementally: ingest first, then identity and linking, then the engine, each with a measurable check. Investigation scripts (read-only Python) live in code/tools for traceability.', 'Small steps with a number after each.'],
  ['5. Testing and evaluation', 'Unit tests, a sample evaluator aiming for 95 to 100%, three custom cases.', 'cmd/eval with five metrics, six custom cases as executable tests, plus a contract validator. Iteration log: 51% to 94% precision, 90.6% to 100% status accuracy.', 'The evaluator found the bugs; the validator found one the evaluator could not.'],
  ['6. Packaging and delivery', 'Auto-generate reports; zip the repository.', 'cmd/report writes both reports from the run; README with setup and run commands; code.zip excludes dataset, .env and databases; log.txt is the chat transcript.', 'Generate documents from data so they cannot drift.'],
  ['7. Operations and maintenance', '(not in the blueprint)', 'A blocked API key and a deleted cache taught: fail loudly, back up paid results, validate before shipping.', 'Add these to the plan next time.'],
];
document.getElementById('sdlctable').innerHTML = `<table><thead><tr><th>Phase</th><th>Blueprint planned</th><th>What actually happened</th><th>Lesson</th></tr></thead><tbody>${SDLC.map((r) => `<tr>${r.map((c) => `<td>${c}</td>`).join('')}</tr>`).join('')}</tbody></table>`;

/* ------------------------------------------------------------------ story */
document.getElementById('storybox').innerHTML = `
<h3>The key was blocked, and the cache was deleted</h3>
<ol>
  <li>To get a clean usage report I deleted the local cache. That cache held the 15 paid vision results.</li>
  <li>The re-run then failed on every Gemini call with HTTP 403 "Your project has been denied access". The pipeline carried on and wrote a valid-looking <code>output.csv</code> built without the images and PDFs and with keyword intent.</li>
  <li>Scoring exposed it: sample status accuracy 100% to 87.5%, step recall 93.3% to 87.0%. This was an accidental <b>ablation</b>: the vision evidence is worth about 12 points of status accuracy.</li>
  <li>The contract validator flagged four COMPLETE cases with no steps; three were the missing vision data, one (case_61) was a genuine time-travel case.</li>
  <li>Fixes: strict mode (an LLM failure now aborts the run), a cache backup before every run, <code>--allow-degraded</code> for deliberate offline runs, and a validator that must pass before submitting.</li>
  <li>After the key was restored, the full pipeline was re-run from an empty cache so the usage report describes a genuine live run, and the results matched the earlier ones exactly.</li>
</ol>
<p><b>The message for the interviewer:</b> I found the problem by measuring, I did not hide it, and I turned it into engineering so it cannot happen silently again.</p>`;

/* ------------------------------------------------------------------ Q&A */
const QA = [
  ['Walk me through the architecture in one minute.', 'Ingest everything into a relational SQLite database, deterministically; classify intent with Gemini; resolve the user and every identifier they ever had; find their records and derive each one\'s state from the latest evidence available at audit time; apply the deletion rules to produce ordered findings, remediation and verification; validate the contract; write output.csv. Gemini only reads images and judges free-text intent.'],
  ['Where is the LLM used, and why not more?', 'Two places: transcribing 15 images/PDFs, and deciding whether a request authorizes deletion. Everything else is code because the grader wants exact operations and order, and code is reproducible, testable and cannot invent an evidence id.'],
  ['How do you stop hallucinated evidence ids?', 'The model never sees the evidence index and never chooses an id. Every cited id is the file_id of a file a query found, and the validator checks each one exists and was captured at or before audit_at.'],
  ['How do you handle audit_at / time travel?', 'Only files captured at or before audit_at are eligible, for both state and citation. A custom test moves every sample\'s audit_at back to 2026-07-01 and checks that no step cites later evidence; a real case (case_61) shows the policy: nothing provable means INCOMPLETE, not COMPLETE.'],
  ['How do you resolve historical identifiers?', 'The audit log is replayed into validity windows per field, and SHA-256 email hashes are derived for every email. 100 of 518 requests used an old email; a test shows an old-email request and a current-email request give identical audits.'],
  ['How do you decide COMPLETE or INCOMPLETE?', 'Per resource, the state at audit time is compared with the rule for its data category (delete, redact, anonymize, retain). Any violation, active data flow or restorable backup makes the case INCOMPLETE; COMPLETE requires every relevant copy to be proven compliant.'],
  ['How do you catch replication drift?', 'Inside one SQL snapshot, a record counts as present if any table, primary or replica, still holds it. Sample case_05 is exactly that: gone from the primary, still on the failover replica.'],
  ['How do you know a resource with no identifier belongs to a user?', 'Honestly: through the deletion worker\'s activity near reported_at, where the worker result must agree with the case reported status, matched globally so each resource belongs to one case. It is a grounded link but not certain: cases that share a reported_at can be swapped, so roughly 12 of 18 accounts and 4 of 7 analytics profiles attribute correctly on the samples. I report it as a limit.'],
  ['What are the weaknesses?', 'Seven expected audit_record ids appear in no supplied file, so those verification steps cannot be recovered; attribution ambiguity as above; the attribution windows were fitted on this data. All are documented in the evaluation report.'],
  ['How did you evaluate?', 'cmd/eval against the 32 samples: 100% status, 93.8% step precision, 93.3% recall, 98.7% field accuracy, 62.5% identical sequences. Six custom cases as executable tests, a contract validator on the 518 rows, and a natural experiment: the 10 non-deletion requests the model found equal the 10 CLASSIFY_REQUEST events.'],
  ['What did it cost and how did you measure it?', 'About 283k tokens, 422 calls, an estimated $0.19, about 547 tokens per request. Every call is a row in llm_usage with exact token counts from the API; the cost is tokens times the configured price, so it is an estimate and the usage report says so.'],
  ['What if Gemini is down?', 'The run stops with a clear error. I chose that on purpose after a blocked key silently produced a weaker output; --allow-degraded exists for deliberate offline runs. Cached results mean a re-run costs nothing once the calls have succeeded.'],
  ['Why Go and SQLite rather than Python and Postgres?', 'Go for footprint and near-compiled speed, one static binary and easy concurrency; SQLite because the problem is relational but batch and single-user, so a server adds nothing and a file is trivial to inspect in DBeaver. Postgres would matter for concurrent services, not here.'],
  ['How is it dynamic? What if the data changes?', 'Tables come from CSV headers, evidence files are discovered by walking the folder, SQL snapshots are read by enumerating their tables, JSON is walked generically and rule actions come from deletion_rules.csv. Re-running rebuilds everything; a small configuration file holds the few things that are knowledge rather than data.'],
  ['What would you do with another week?', 'Resolve attribution ambiguity with more signals (for example matching the order of paired worker events), add a second model as an independent check on the vision transcription, and a review UI over the database.'],
  ['How did you use AI coding tools?', 'I designed the SDLC, chose the stack and the Gemini models; the coding agent implemented under that direction, every turn is recorded in log.txt, and I reviewed the results in DBeaver and against the samples. I can explain every stage because each one has a measurable check.'],
];
document.getElementById('qalist').innerHTML = QA.map(([q, a], i) => card(i + 1, q, '', { Answer: a })).join('');

/* ------------------------------------------------------------------ limits */
document.getElementById('limitbox').innerHTML = `
<div class="pillar"><h3>Limits (say them first)</h3><ul>
  <li>Seven retained audit-record ids in the sample answers appear in no supplied file; each loses one verification step.</li>
  <li>Cases that share a reported_at can swap an unidentified account or analytics profile.</li>
  <li>Attribution windows and the analytics offset were fitted on observed data; a different cadence needs re-checking (one command).</li>
  <li>Cost is an estimate; token counts are exact.</li>
  <li>The 32 samples contain no NOT_APPLICABLE case, so intent is validated by the natural experiment and custom tests rather than a key.</li>
</ul></div>
<div class="pillar"><h3>Next steps</h3><ul>
  <li>More attribution signals to break reported_at ties.</li>
  <li>A second, independent model to cross-check the vision transcription.</li>
  <li>A small review UI over the database for human audit of INCOMPLETE cases.</li>
  <li>A CI job running go test and the validator on every change.</li>
</ul></div>`;

/* ------------------------------------------------------------------ wiring */
document.addEventListener('click', (e) => {
  const b = e.target.closest('.card > button');
  if (!b) return;
  const c = b.parentElement; c.classList.toggle('open');
  b.setAttribute('aria-expanded', c.classList.contains('open'));
});
drawDiagram();
select('s3');
