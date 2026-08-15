import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
for t in ('context_operations','context_checkpoints','context_sessions'):
    print(t, ':', [c[1] for c in con.execute(f'PRAGMA table_info({t})')])
print()
ws='workspace-2abf6314f6cd7856'
sid='NN75RTMUF7XIOLMVYGNVR5VIMY'
print('=== NN75 context_operations (all, rowid order) ===')
rows = list(con.execute('SELECT rowid, session_id, operation_id, kind, result, created_at FROM context_operations WHERE session_id=? ORDER BY rowid', (sid,)))
print('total ops:', len(rows))
for r in rows:
    d = dict(zip(('rowid','session_id','operation_id','kind','result','created_at'), r))
    msg = str(d.get('result',''))[:180].replace('\n',' ')
    print(' ', d['rowid'], d['kind'], d['created_at'], msg)
print()
print('=== NN75 checkpoints ===')
rows = list(con.execute('SELECT * FROM context_checkpoints WHERE session_id=? ORDER BY rowid', (sid,)))
cols = [c[1] for c in con.execute('PRAGMA table_info(context_checkpoints)')]
print('total checkpoints:', len(rows))
for r in rows:
    d = dict(zip(cols, r))
    print(' ', d.get('rowid'), d.get('created_at'), 'rev', d.get('revision'), 'turn', d.get('turn_id'), 'msgs', d.get('ctx_msgs'), 'bytes', d.get('active_context_bytes'), 'algo', d.get('algo'), 'complete', d.get('complete'), 'cp', str(d.get('checkpoint_id'))[:20])
