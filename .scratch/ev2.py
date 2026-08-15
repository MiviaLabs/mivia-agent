import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
sid='NGD4HO6DCVUSNH7A3QAHOJS63M'
print('=== events ledger mentioning NGD4HO6 (run_id join on payload) ===')
cols = [c[1] for c in con.execute('PRAGMA table_info(events)')]
print('cols:', cols)
rows = list(con.execute('SELECT * FROM events WHERE payload LIKE ? ORDER BY rowid LIMIT 200', ('%'+sid+'%',)))
print('total matches:', len(rows))
for r in rows:
    d = dict(zip(cols, r))
    p = str(d.get('payload',''))[:200].replace('\n',' ')
    print(d.get('run_id'), d.get('sequence'), d.get('kind'), d.get('created_at'), p)
