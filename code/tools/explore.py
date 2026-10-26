import sqlite3,sys,json
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
def q(s,*a): return db.execute(s,a).fetchall()
def brief(raw,maxlen=230):
    d=json.loads(raw)
    drop={'resource_id','timestamp','level','instance','pod','region','trace_id','service','attempt','integrity_seal','logger'}
    s=' '.join(f'{k}={v}' for k,v in d.items() if k not in drop and not isinstance(v,(dict,list)))
    for k,v in d.items():
        if isinstance(v,(dict,list)) and k not in drop: s+=f' {k}={json.dumps(v)[:120]}'
    return s[:maxlen]
for cid in sys.argv[1:]:
    c=q("select email,requested_at,reported_status,reported_at,audit_at,request_text from sample_deletion_requests where case_id=?",cid)[0]
    uid=q("select i.user_id from sample_deletion_requests s join identifiers i on i.kind='email' and i.norm_value=lower(s.email) where s.case_id=?",cid)[0][0]
    print(f"\n######## {cid} user={uid} email={c[0]} req={c[1]} rep={c[2]}@{c[3]} audit={c[4]}\n  TEXT: {c[5][:200]}")
    steps=q("select step_no,phase,system_id,resource_id,operation,evidence_id from sample_output_steps where case_id=? order by idx",cid)
    for s in steps: print('  S',*s)
    seen=set()
    for s in steps:
        res=s[3]
        if res in seen: continue
        seen.add(res)
        print(f"  -- {res} observations:")
        for r in q("select d.file_id,d.kind,d.captured_at,r.observed_at,r.locator,r.raw_json,(select group_concat(s.table_name||'/'||ifnull(s.topology,'')) from evidence_snapshots s where s.snapshot_id=r.snapshot_id) from evidence_records r join evidence_documents d using(doc_id) where r.resource_id=? order by r.observed_at",res):
            flag='' if (r[3] or '')<=c[4] else ' [AFTER AUDIT]'
            print(f"     {r[0]} {r[1]} obs={r[3]} {r[6] or ''} :: {brief(r[5])}{flag}")
        for r in q("select activity_id,event_time,event_type,reported_result from system_activity where resource_id=? order by event_time",res):
            print('     ACT',*r)
