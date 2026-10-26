import sqlite3,collections,datetime as dt
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
q=lambda s,*a: db.execute(s,a).fetchall()
T=lambda s: dt.datetime.strptime(s,'%Y-%m-%dT%H:%M:%SZ')
owner=collections.defaultdict(set)
for r,u in q("select resource_id,user_id from resource_links where confidence>=0.9"): owner[r].add(u)
for pref in ('account','analytics'):
    print('=====',pref)
    ranks=collections.Counter()
    for cid,uid,req,rep,aud in q("select s.case_id,i.user_id,s.requested_at,s.reported_at,s.audit_at from sample_deletion_requests s join identifiers i on i.kind='email' and i.norm_value=lower(s.email) order by 1"):
        gold={r[0] for r in q("select distinct resource_id from sample_output_steps where case_id=? and resource_id like ? and resource_id not like 'audit%'",cid,pref+'_%')}
        gold={g for g in gold if not (owner.get(g) and uid in owner[g])}   # only the unattributed ones
        if not gold: continue
        R=T(rep)
        # wide window around reported_at
        ev=q("select resource_id,event_time,event_type,reported_result from system_activity where source_system='deletion_worker' and resource_id like ? and event_time between ? and ?",pref+'_%',(R-dt.timedelta(days=4)).strftime('%Y-%m-%dT%H:%M:%SZ'),(R+dt.timedelta(days=1)).strftime('%Y-%m-%dT%H:%M:%SZ'))
        cands=[(abs((T(t)-R).total_seconds()),(T(t)-R).total_seconds(),r,et,rr) for r,t,et,rr in ev if not owner.get(r)]
        cands.sort()
        for g in gold:
            pos=[i for i,c in enumerate(cands) if c[2]==g]
            gd=[c for c in cands if c[2]==g]
            print(f"{cid} gold={g} rank={pos[0]+1 if pos else None}/{len(cands)} delta_min={(round(gd[0][1]/60) if gd else None)}")
            ranks[pos[0]+1 if pos else 0]+=1
    print(ranks)
