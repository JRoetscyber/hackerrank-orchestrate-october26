import sqlite3,collections
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
q=lambda s,*a: db.execute(s,a).fetchall()
owner=collections.defaultdict(set)
for res,u in q("select r.resource_id, ri.user_id from record_identifiers ri join evidence_records r using(record_id) where r.resource_id is not null"):
    owner[res].add(u)
tot=collections.Counter(); 
import re
valid=re.compile(r'^(account|search|support|analytics|audit_record|backup|data_flow|export|legal_hold|tombstone|cache|marketing|email_delivery|telemetry)_')
for cid,uid,aud in q("select s.case_id,i.user_id,s.audit_at from sample_deletion_requests s join identifiers i on i.kind='email' and i.norm_value=lower(s.email) order by 1"):
    E={r[0] for r in q("select distinct resource_id from sample_output_steps where case_id=?",cid)}
    Bany={r for r,u in owner.items() if uid in u and valid.match(r)}
    miss=sorted(E-Bany); extra=sorted(Bany-E)
    tot['E']+=len(E); tot['miss']+=len(miss); tot['extra']+=len(extra)
    print(cid,'miss',miss,'extra',extra)
print(tot)
