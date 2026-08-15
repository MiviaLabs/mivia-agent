import sqlite3
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
ws='workspace-2abf6314f6cd7856'
sid='NGD4HO6DCVUSNH7A3QAHOJS63M'
print('=== events ledger for NGD4HO6 ===')
rows = list(con.execute('SELECT sequence, kind, created_at, payload FROM events WHERE workspace_id=? AND session_id=? ORDER BY sequence', (ws,sid)))
print('total events:', len(rows))
for r in rows[:100]:
    seq, kind, created, payload = r
    msg = ''
    if payload:
        import json
        try:
            obj = json.loads(payload)
            if isinstance(obj, dict):
                msg = json.dumps(obj)[:200]
            else:
                msg = str(payload[:200])
        except Exception:
            msg = str(payload[:200])
    print(seq, kind, created, msg.replace('\n',' '))
