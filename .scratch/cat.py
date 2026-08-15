import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
ws='workspace-2abf6314f6cd7856'
cols = [c[1] for c in con.execute('PRAGMA table_info(chat_sessions)')]
print('chat_sessions cols:', cols)
for sid in ['NN75RTMUF7XIOLMVYGNVR5VIMY','NGD4HO6DCVUSNH7A3QAHOJS63M']:
    print('---', sid)
    for r in con.execute('SELECT * FROM chat_sessions WHERE workspace_id=? AND name=?', (ws,sid)):
        d = dict(zip(cols,r))
        mb = d.get('messages')
        n = len(json.loads(mb)) if mb else 0
        d['messages_len'] = len(mb) if mb else 0
        d['messages_count'] = n
        print(' ', json.dumps(d, default=str)[:600])
