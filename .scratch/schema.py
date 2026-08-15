import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
print('chat_sessions cols:', [c[1] for c in con.execute('PRAGMA table_info(chat_sessions)')])
print('context_sessions cols:', [c[1] for c in con.execute('PRAGMA table_info(context_sessions)')])
print('context_checkpoints cols:', [c[1] for c in con.execute('PRAGMA table_info(context_checkpoints)')])
print('context_source_events cols:', [c[1] for c in con.execute('PRAGMA table_info(context_source_events)')])
print('events cols:', [c[1] for c in con.execute('PRAGMA table_info(events)')])
