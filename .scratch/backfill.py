import sqlite3
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
print('=== workspace_ids in chat_sessions ===')
for r in con.execute('SELECT workspace_id, count(*) FROM chat_sessions GROUP BY workspace_id ORDER BY 2 DESC'):
    print(' ', r)
print()
print('=== rows named NN75*/NGD4HO6* anywhere ===')
for r in con.execute("SELECT workspace_id, name, session_id, message_count, updated_at FROM chat_sessions WHERE name LIKE 'NN75%' OR name LIKE 'NGD4HO6%'"):
    print(' ', r)
print()
print('=== context_sessions rows for NN75/NGD4HO6 ===')
for r in con.execute("SELECT workspace_id, session_id, source_sequence, tombstoned, COALESCE(title,'') FROM context_sessions WHERE session_id LIKE 'NN75%' OR session_id LIKE 'NGD4HO6%'"):
    print(' ', r)
