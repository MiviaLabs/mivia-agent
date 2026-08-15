import sqlite3
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
print('user_version:', con.execute('PRAGMA user_version').fetchone()[0])
print('migrations:', list(con.execute('SELECT version, dirty FROM context_schema_migrations ORDER BY version')))
print('chat_sessions has session_id col:', any(r[1]=='session_id' for r in con.execute('PRAGMA table_info(chat_sessions)')))
print('count projections in live db:', list(con.execute("SELECT count(*) FROM chat_sessions WHERE session_id IS NOT NULL"))[0][0])
