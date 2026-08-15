import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
print('=== most recent events by run_id ===')
cols = [c[1] for c in con.execute('PRAGMA table_info(events)')]
rows = list(con.execute('SELECT run_id, MAX(created_at) AS last, COUNT(*) AS n FROM events GROUP BY run_id ORDER BY last DESC LIMIT 12'))
for r in rows:
    print(' run', r[0], 'last', r[1], 'events', r[2])
print()
print('=== run payloads with session_id for recent runs ===')
for r in con.execute('SELECT run_id, created_at, payload FROM events WHERE payload LIKE ? ORDER BY created_at DESC LIMIT 6', ('%session_id%',)):
    p = str(r[2])
    i = p.find('session_id')
    print(' ', r[0], r[1], p[max(0,i-40):i+70].replace('\n',' '))
print()
print('=== NN75 catalog messages (roles + first/last) ===')
ws='workspace-2abf6314f6cd7856'
sid='NN75RTMUF7XIOLMVYGNVR5VIMY'
mb = list(con.execute('SELECT messages FROM chat_sessions WHERE workspace_id=? AND name=?', (ws,sid)))[0][0]
cat = json.loads(mb)
from collections import Counter
print(' count', len(cat), dict(Counter(m.get('role','?') for m in cat)))
for m in cat[:3]:
    print('  first:', m.get('role'), repr((m.get('content') or '')[:80]))
for m in cat[-3:]:
    print('  last:', m.get('role'), m.get('created_at',''), repr((m.get('content') or '')[:80]))
print()
print('=== NN75 active checkpoint rev32 content (roles + first/last) ===')
ac = list(con.execute("SELECT active_context FROM context_checkpoints WHERE workspace_id=? AND session_id=? AND checkpoint_id='ctxc_90876f84c96c04e9924207955bafbe1b64bca1a1e029060c2bdb5219301f6be4'", (ws,sid)))[0][0]
act = json.loads(ac)
print(' count', len(act), dict(Counter(m.get('role','?') for m in act)))
for m in act[:3]:
    print('  first:', m.get('role'), repr((m.get('content') or '')[:80]))
for m in act[-3:]:
    print('  last:', m.get('role'), repr((m.get('content') or '')[:80]))
