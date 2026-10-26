import sqlite3,collections
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
q=lambda s,*a: db.execute(s,a).fetchall()
tot=collections.Counter()
for cid,uid,req,rep,aud in q("select s.case_id,i.user_id,s.requested_at,s.reported_at,s.audit_at from sample_deletion_requests s join identifiers i on i.kind='email' and i.norm_value=lower(s.email) order by 1"):
    E={r[0] for r in q("select distinct resource_id from sample_output_steps where case_id=?",cid)}
    A={r[0] for r in q("select resource_id from system_activity where event_time between ? and ? and source_system='deletion_worker'",req,rep)}
    A2={r[0] for r in q("select resource_id from system_activity where event_time between ? and ?",req,aud)}
    B={r[0] for r in q("""select distinct r.resource_id from record_identifiers ri join evidence_records r using(record_id) join evidence_documents d using(doc_id)
        where ri.user_id=? and r.resource_id is not null and d.captured_at<=?""",uid,aud)}
    miss=E-(A|B); 
    tot['E']+=len(E); tot['missing']+=len(miss); tot['A_only_noise']+=len(A-E-B); tot['B_extra']+=len(B-E)
    print(cid,f"E={len(E)} A={len(A)} B={len(B)} missing={sorted(miss)} A_extra={len(A-E)} B_extra={sorted(B-E)[:4]}")
print(tot)
