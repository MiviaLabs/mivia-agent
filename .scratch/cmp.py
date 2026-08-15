import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
ws='workspace-2abf6314f6cd7856'
for sid in ['NN75RTMUF7XIOLMVYGNVR5VIMY','NGD4HO6DCVUSNH7A3QAHOJS63M','ETW5MD7XRV5HYRZRMQIH52OQOA']:
    print('---', sid)
    for r in con.execute('SELECT name, model, provider, created_at, updated_at, turn_count, token_count, message_count, instance_id, length(messages) FROM chat_sessions WHERE workspace_id=? AND name=?', (ws,sid)):
        print('  catalog:', r)
    for r in con.execute('SELECT session_id, session_revision, durable_revision, source_sequence, provider, model, active_checkpoint_id, created_at FROM context_sessions WHERE workspace_id=? AND session_id=?', (ws,sid)):
        print('  store:  ', r)
    n = list(con.execute('SELECT COUNT(*) FROM context_source_events WHERE workspace_id=? AND session_id=?', (ws,sid)))[0][0]
    print('  source events:', n)
