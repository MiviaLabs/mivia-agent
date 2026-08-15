import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
ws='workspace-2abf6314f6cd7856'
sid='NGD4HO6DCVUSNH7A3QAHOJS63M'
print('--- NGD4HO6 checkpoints with active_context msg counts ---')
ccols = [c[1] for c in con.execute('PRAGMA table_info(context_checkpoints)')]
for r in con.execute('SELECT * FROM context_checkpoints WHERE workspace_id=? AND session_id=? ORDER BY session_revision', (ws,sid)):
    d = dict(zip(ccols,r))
    ac = d.get('active_context')
    n = len(json.loads(ac)) if ac else 0
    roles = {}
    if ac:
        for m in json.loads(ac):
            roles[m.get('role','?')] = roles.get(m.get('role','?'),0)+1
    print(' rev', d.get('session_revision'), 'src', d.get('source_start'),'-',d.get('source_end'), 'turn', d.get('turn_id'),
          'algo', d.get('algorithm'), 'msgs', n, roles, 'bytes', len(ac) if ac else 0, d.get('created_at'))

print()
print('--- NGD4HO6 last 12 catalog messages (index, role, created_at, content head) ---')
cat = json.loads(list(con.execute('SELECT messages FROM chat_sessions WHERE workspace_id=? AND name=?', (ws,sid)))[0][0])
for i in range(len(cat)-12, len(cat)):
    m = cat[i]
    print(i, m.get('role'), m.get('created_at',''), repr((m.get('content') or '')[:110]))
