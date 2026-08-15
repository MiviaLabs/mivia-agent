import sqlite3
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
print('=== recent task events mentioning verifier/auditor (last 2h) ===')
rows = list(con.execute("""SELECT run_id, created_at, kind, substr(payload,1,200) FROM events
WHERE created_at >= '2026-08-15 00:00:00' ORDER BY created_at"""))
for r in rows:
    p = str(r[3])
    tag = 'VERIFIER' if 'verifier' in p.lower() else ('AUDITOR' if 'auditor' in p.lower() else '')
    print(' ', r[0][:30], r[1], r[2], tag, p[:120].replace('\n',' '))
print()
print('=== latest run_created events overall ===')
for r in con.execute("SELECT run_id, created_at, substr(payload,1,120) FROM events WHERE kind='run_created' ORDER BY created_at DESC LIMIT 6"):
    print(' ', r[0][:30], r[1], str(r[2])[:110].replace('\n',' '))
