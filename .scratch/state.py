import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
ws='workspace-2abf6314f6cd7856'

for sid in ['NN75RTMUF7XIOLMVYGNVR5VIMY','NGD4HO6DCVUSNH7A3QAHOJS63M']:
    print('='*100)
    print('SESSION', sid)
    print('--- context_sessions row ---')
    cols = [c[1] for c in con.execute('PRAGMA table_info(context_sessions)')]
    for r in con.execute('SELECT * FROM context_sessions WHERE workspace_id=? AND session_id=?', (ws,sid)):
        d = dict(zip(cols,r))
        print(' ', json.dumps(d, default=str))
    print('--- context_checkpoints (all) ---')
    ccols = [c[1] for c in con.execute('PRAGMA table_info(context_checkpoints)')]
    for r in con.execute('SELECT * FROM context_checkpoints WHERE workspace_id=? AND session_id=? ORDER BY session_revision', (ws,sid)):
        d = dict(zip(ccols,r))
        ac = d.get('active_context')
        n = len(json.loads(ac)) if ac else 0
        print('  rev', d.get('session_revision'), d.get('checkpoint_id','')[:16], 'src', d.get('source_start'), '-', d.get('source_end'),
              'turn', d.get('turn_id'), 'complete', d.get('complete'), 'algo', d.get('algorithm'),
              'ctx_msgs', n, 'bytes', len(ac) if ac else 0, d.get('created_at'))
    print('--- chat_sessions catalog row ---')
    for r in con.execute('SELECT name, title, model, provider, created_at, updated_at, turn_count, message_count, token_count, instance_id, length(messages) FROM chat_sessions WHERE workspace_id=? AND name=?', (ws,sid)):
        print(' ', r)
