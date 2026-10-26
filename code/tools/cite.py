import sqlite3,collections
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
q=lambda s,*a: db.execute(s,a).fetchall()
res=collections.Counter(); bad=[]
for cid,aud in q("select case_id,audit_at from sample_deletion_requests order by 1"):
    for ph,sysid,rid,op,ev in q("select phase,system_id,resource_id,operation,evidence_id from sample_output_steps where case_id=? and evidence_id<>'' order by idx",cid):
        files=q("""select distinct d.file_id,d.captured_at from evidence_records r join evidence_documents d using(doc_id)
                   where r.resource_id=? and d.captured_at<=?  order by d.captured_at desc""",rid,aud)
        ids=[f[0] for f in files]
        if not ids: res['no_obs']+=1; bad.append((cid,rid,op,ev,'NO OBSERVATION')); continue
        if ev==ids[0]: res['latest']+=1
        elif ev in ids: res['older_obs']+=1; bad.append((cid,rid,op,ev,'older; latest=%s all=%s'%(ids[0],ids)))
        else: res['not_in_obs']+=1; bad.append((cid,rid,op,ev,'not an observer; observers=%s'%ids))
print(res)
for b in bad[:60]: print(*b)
