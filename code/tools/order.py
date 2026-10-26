import sqlite3,collections,itertools
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
q=lambda s,*a: db.execute(s,a).fetchall()
prec=collections.defaultdict(set)
ties=collections.defaultdict(list)
for (cid,) in q("select distinct case_id from sample_output_steps"):
    for ph in ('FINDING','REMEDIATION','VERIFICATION'):
        seq=q("select operation,system_id,resource_id from sample_output_steps where case_id=? and phase=? order by idx",cid,ph)
        ops=[]
        for o,s,r in seq:
            if not ops or ops[-1]!=o: ops.append(o)
        for a,b in itertools.combinations(ops,2): prec[(ph,a)].add(b)
        # tie order
        for op,g in itertools.groupby(seq,key=lambda x:x[0]):
            g=list(g)
            if len(g)>1: ties[(ph,op)].append([(s,r) for _,s,r in g])
for ph in ('FINDING','REMEDIATION','VERIFICATION'):
    ops=sorted({o for (p,o) in prec if p==ph}|{b for (p,o),v in prec.items() if p==ph for b in v},key=lambda o:-len(prec.get((ph,o),())))
    print(ph,' > '.join(ops))
    bad=[(a,b) for (p,a),v in prec.items() if p==ph for b in v if a in prec.get((ph,b),())]
    print('  contradictions:',bad)
print()
for k,v in ties.items():
    print(k)
    for t in v[:6]: print('   ',t)
