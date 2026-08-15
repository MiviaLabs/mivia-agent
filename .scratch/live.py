import sqlite3
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
print('=== live catalog rows: session_id column state (NN75 / NGD4HO6) ===')
for r in con.execute("SELECT name, COALESCE(session_id,'<NULL>'), message_count, length(messages), updated_at FROM chat_sessions WHERE name IN ('NN75RTMUF7XIOLMVYGNVR5VIMY','NGD4HO6DCVUSNH7A3QAHOJS63M')"):
    print(' ', r)
print()
print('=== live context_sessions rows ===')
for r in con.execute("SELECT session_id, session_revision, source_sequence, active_checkpoint_id, tombstoned, title FROM context_sessions ORDER BY session_revision DESC LIMIT 6"):
    print(' ', r)
