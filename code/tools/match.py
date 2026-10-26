import sqlite3,collections,datetime as dt
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
q=lambda s,*a: db.execute(s,a).fetchall()
T=lambda s: dt.datetime.strptime(s,'%Y-%m-%dT%H:%M:%SZ')
owner=collections.defaultdict(set); links=collections.defaultdict(set)
for r,u in q("select resource_id,user_id from resource_links where confidence>=0.9"): owner[r].add(u); links[u].add(r)
cases=q("select case_id,email,requested_at,reported_status,reported_at from deletion_requests union all select case_id,email,requested_at,reported_status,reported_at from sample_deletion_requests")
resmap={'COMPLETE':'SUCCESS','PARTIALLY_COMPLETE':'PARTIAL','NOT_STARTED':'FAILED'}
def run(pref,lo,hi,use_result,center=None):
    events=[(r,T(t),rr) for r,t,rr in q("select resource_id,event_time,reported_result from system_activity where source_system='deletion_worker' and event_type in ('DELETE_RECORD','ANONYMIZE_RECORD') and resource_id like ?",pref+'_%') if not owner.get(r)]
    edges=[]; need=set()
    for cid,email,req,rs,rep in cases:
        us=[u for (u,) in q("select distinct user_id from identifiers where kind='email' and norm_value=lower(?)",email)]
        if any(r.startswith(pref+'_') for u in us for r in links[u]): continue
        need.add(cid)
        R=T(rep); Q=T(req)
        for r,t,rr in events:
            d=(t-R).total_seconds()/60
            if not(lo<=d<=hi) or t<Q: continue
            if use_result and rr!=resmap[rs]: continue
            c=abs(d-center) if center is not None else abs(d)
            edges.append((c,cid,r))
    edges.sort(); got={}; used=set()
    for c,cid,r in edges:
        if cid in got or r in used: continue
        got[cid]=r; used.add(r)
    ok=n=0
    for cid in [c for c in got]: pass
    res=collections.Counter()
    for cid in need:
        if not cid.startswith('case_') : continue
        gold={x for (x,) in q("select distinct resource_id from sample_output_steps where case_id=? and resource_id like ?",cid,pref+'_%')} if int(cid.split('_')[1])<=32 else None
        if gold is None: continue
        gold={g for g in gold if not owner.get(g)}
        if not gold: res['no_gold_unattrib']+=1 if cid in got else 0; continue
        res['hit' if got.get(cid) in gold else ('wrong' if cid in got else 'none')]+=1
    return res
print('account  |d| no-result       ',run('account',-120,15,False))
print('account  |d| result         ',run('account',-120,15,True))
print('account  window[-80,10]res  ',run('account',-80,10,True))
print('account  center-58 [-80,-40]',run('account',-80,-40,True,-58.5))
print('analytics |d|               ',run('analytics',-60,15,False))
print('analytics center -28.5      ',run('analytics',-35,-22,False,-28.5))
