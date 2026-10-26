import sqlite3,json,collections
db=sqlite3.connect('file:data/audit.db?mode=ro',uri=True)
noise={'timestamp','level','instance','pod','region','trace_id','service','attempt','duration_ms','logger','severity','http_status','took_ms','shard','wait_ms','pool','route','reason','retry_in_ms','dependency','instance_id','requestId','function','message','host','observed_at','updated_at','recorded_at','captured_at','integrity_seal','event_time','resource_id'}
sig=collections.defaultdict(list)
for rid,sys_,coll,ext,raw,doc in db.execute("select r.record_id,r.system_id,r.collection,r.extractor,r.raw_json,d.file_id from evidence_records r join evidence_documents d using(doc_id) where r.resource_id is not null and r.collection not in ('visible_text','text_lines')"):
    d=json.loads(raw)
    pref=d.get('resource_id','').rstrip('0123456789').rstrip('_')
    keys=tuple(sorted(k for k in d if k not in noise))
    ev=d.get('event','')
    sig[(pref,ext if ext in('logfmt','gemini_vision') else 'struct',ev,keys)].append((doc,raw))
rows=sorted(sig.items(),key=lambda x:(x[0][0],-len(x[1])))
for (pref,ext,ev,keys),l in rows:
    if len(l)<2 and ext=='gemini_vision': continue
    print(f"{pref:18} {ext:7} n={len(l):4} ev={ev} keys={','.join(keys)[:150]}")
