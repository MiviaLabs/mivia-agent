import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
ws='workspace-2abf6314f6cd7856'
sid='NN75RTMUF7XIOLMVYGNVR5VIMY'

def show(blob, label):
    m = json.loads(blob)
    print(f'--- {label}: {len(m)} msgs ---')
    for i, x in enumerate(m):
        c = (x.get('content') or '')
        name = x.get('name') or ''
        print(f'  [{i}] role={x.get("role")} name={name!r} {c[:100]!r}')
    print()

cat = list(con.execute('SELECT messages FROM chat_sessions WHERE workspace_id=? AND name=?', (ws,sid)))[0][0]
cps = list(con.execute('SELECT checkpoint_id, active_context, created_at, turn_id, session_revision, algorithm, source_start, source_end, complete FROM context_checkpoints WHERE workspace_id=? AND session_id=? ORDER BY session_revision', (ws,sid)))
print('NN75 checkpoints:')
for (checkpoint_id, active_context, created_at, turn_id, rev, algo, s, e, complete) in cps:
    n = len(json.loads(active_context))
    print(f'  rev={rev} turn={turn_id} msgs={n} bytes={len(active_context)} {created_at} algo={algo} src={s}..{e} complete={complete} {checkpoint_id[:24]}')
print()
byrev = {r[4]: r for r in cps}
rev30 = byrev[30][1]
rev31 = byrev[31][1]
rev32 = byrev[32][1]
print('catalog == rev30 active_context bytes?', cat == rev30)
print('catalog bytes', len(cat), 'rev30 bytes', len(rev30))
show(rev31, 'rev31 (compact, 00:24:56)')
show(rev32, 'rev32 (turn "i just compacted context", 00:25:40)')
print('=== context_sessions NN75 ===')
for r in con.execute('SELECT session_id, session_revision, durable_revision, source_sequence, active_checkpoint_id, tombstoned, title FROM context_sessions WHERE workspace_id=? AND session_id=?', (ws,sid)):
    print(' ', r)
print()
print('=== NN75 source events near end (sequence, kind, role, payload_size, provenance) ===')
for r in con.execute("SELECT sequence, kind, role, payload_size, provenance FROM context_source_events WHERE workspace_id=? AND session_id=? AND sequence>=380 ORDER BY sequence", (ws,sid)):
    print(' ', r)
