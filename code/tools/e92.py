import sqlite3,json
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
rows=db.execute("""select r.observed_at,r.resource_id,r.collection,r.raw_json,(select group_concat(distinct ri.user_id) from record_identifiers ri where ri.record_id=r.record_id)
 from evidence_records r join evidence_documents d using(doc_id) where d.file_id='evidence_92' and r.resource_id is not null order by r.observed_at""").fetchall()
exp={}
for cid,res in db.execute("select s.case_id, o.resource_id from sample_output_steps o join sample_deletion_requests s using(case_id)"): exp.setdefault(res,set()).add(cid)
for t,res,coll,raw,u in rows:
    d=json.loads(raw)
    extra=d.get('subject_reference') or d.get('status_code') or d.get('audience_cohort')
    print(t[11:19],f"{res:20}",f"{coll:16}",f"{str(extra):22}",f"link={u}",f"cases={sorted(exp.get(res,[]))}")
