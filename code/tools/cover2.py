import sqlite3,collections,re
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
q=lambda s,*a: db.execute(s,a).fetchall()
# resource -> set of users it is evidence-linked to (any time)
owner=collections.defaultdict(set)
for res,u in q("select r.resource_id, ri.user_id from record_identifiers ri join evidence_records r using(record_id) where r.resource_id is not null"):
    owner[res].add(u)
tot=collections.Counter()
for cid,uid,req,rep,aud in q("select s.case_id,i.user_id,s.requested_at,s.reported_at,s.audit_at from sample_deletion_requests s join identifiers i on i.kind='email' and i.norm_value=lower(s.email) order by 1"):
    E={r[0] for r in q("select distinct resource_id from sample_output_steps where case_id=?",cid)}
    B={r[0] for r in q("select distinct r.resource_id from record_identifiers ri join evidence_records r using(record_id) join evidence_documents d using(doc_id) where ri.user_id=? and r.resource_id is not null",uid)}
    acts=q("select resource_id,event_type,reported_result,details,event_time from system_activity where event_time between ? and ? and source_system='deletion_worker'",req,rep)
    for res,et,rr,det,t in acts:
        inE=res in E; inB=res in B
        other = bool(owner.get(res)) and uid not in owner[res]
        k=('E' if inE else 'notE', 'linkedme' if inB else ('linkedother' if other else 'unlinked'))
        tot[k]+=1
        if inE and not inB: print(cid,'E-not-B-via-activity',res,et,rr,det[:90])
print(tot)
print('-------- unlinked noise by type')
c=collections.Counter(); ex=collections.defaultdict(list)
for cid,uid,req,rep,aud in q("select s.case_id,i.user_id,s.requested_at,s.reported_at,s.audit_at from sample_deletion_requests s join identifiers i on i.kind='email' and i.norm_value=lower(s.email) order by 1"):
    E={r[0] for r in q("select distinct resource_id from sample_output_steps where case_id=?",cid)}
    for res,et,rr,det,t,tgt in q("select resource_id,event_type,reported_result,details,event_time,target_system from system_activity where event_time between ? and ? and source_system='deletion_worker'",req,rep):
        if res in E or owner.get(res): continue
        c[(et,tgt)]+=1; ex[cid].append((res,et,rr,t[5:16]))
for k,v in sorted(c.items(),key=lambda x:-x[1]): print(k,v)
for cid in ('case_21','case_27','case_03'): print(cid,ex[cid][:12])
