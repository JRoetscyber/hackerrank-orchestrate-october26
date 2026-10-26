import sqlite3,collections,datetime as dt,statistics as st
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
q=lambda s,*a: db.execute(s,a).fetchall()
T=lambda s: dt.datetime.strptime(s,'%Y-%m-%dT%H:%M:%SZ')
links=collections.defaultdict(set)
for r,u in q("select resource_id,user_id from resource_links where confidence>=0.9"): links[u].add(r)
acts=collections.defaultdict(list)
for r,t,et,rr in q("select resource_id,event_time,event_type,reported_result from system_activity where source_system='deletion_worker'"): acts[r].append((t,et,rr))
rows=q("select case_id,email,reported_at from deletion_requests union all select case_id,email,reported_at from sample_deletion_requests")
d=collections.defaultdict(list)
for cid,email,rep in rows:
    us=[u for (u,) in q("select distinct user_id from identifiers where kind='email' and norm_value=lower(?)",email)]
    for u in us:
        for r in links[u]:
            p=r.rstrip('0123456789').rstrip('_')
            for t,et,rr in acts.get(r,[]):
                d[(p,et)].append((T(t)-T(rep)).total_seconds()/60)
for k,v in sorted(d.items(),key=lambda x:-len(x[1])):
    if len(v)<5: continue
    v.sort(); n=len(v)
    print(f"{k[0]:14}{k[1]:18} n={n:4} p5={v[int(.05*n)]:7.1f} p25={v[n//4]:7.1f} med={v[n//2]:7.1f} p75={v[3*n//4]:7.1f} p95={v[int(.95*n)]:7.1f}")
