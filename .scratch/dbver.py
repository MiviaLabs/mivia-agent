import sqlite3
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
print('user_version:', con.execute('PRAGMA user_version').fetchone()[0])
print('has chat_sessions.session_id:', any(r[1]=='session_id' for r in con.execute('PRAGMA table_info(chat_sessions)')))
print('has v11 contract:', con.execute("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='chat_sessions_v11_contract'").fetchone()[0])
print('migrations:', list(con.execute('SELECT * FROM context_schema_migrations ORDER BY version')))
print()
print('=== last catalog writes (chat_sessions) per name ===')
for r in con.execute("SELECT name, updated_at, message_count, length(messages) FROM chat_sessions ORDER BY updated_at DESC LIMIT 8"):
    print(' ', r)
