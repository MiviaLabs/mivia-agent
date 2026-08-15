import sqlite3, json
con = sqlite3.connect('file:/home/mac/.mivia/context.db?mode=ro', uri=True)
ws='workspace-2abf6314f6cd7856'
print('=== ALL SESSIONS: catalog vs store active checkpoint vs source events ===')
rows = list(con.execute('''
SELECT cs.name, cs.updated_at, cs.turn_count, cs.message_count, cs.token_count, length(cs.messages),
       ctx.session_id, ctx.session_revision, ctx.source_sequence, ctx.active_checkpoint_id,
       (SELECT length(active_context) FROM context_checkpoints cc WHERE cc.checkpoint_id=ctx.active_checkpoint_id AND cc.workspace_id=ctx.workspace_id),
       (SELECT COUNT(*) FROM context_source_events e WHERE e.workspace_id=ctx.workspace_id AND e.session_id=ctx.session_id)
FROM chat_sessions cs
LEFT JOIN context_sessions ctx ON ctx.workspace_id=cs.workspace_id AND ctx.session_id=cs.name AND ctx.tombstoned=0
WHERE cs.workspace_id=?
ORDER BY cs.updated_at DESC
''', (ws,)))
for r in rows:
    cat_msgs = r[4]  # token_count
    print('cat:', r[0][:16], 'upd', r[1], 'turns', r[2], 'msgs', r[3], 'tokens', r[4], 'blob', r[5],
          '| store:', (r[6] or '')[:12], 'rev', r[7], 'src', r[8],
          '| active_ctx_bytes', r[10], 'src_events', r[11])
print()
print('=== sessions in store WITHOUT catalog row ===')
for r in con.execute('''SELECT session_id, session_revision, source_sequence, active_checkpoint_id FROM context_sessions WHERE workspace_id=? AND tombstoned=0 AND instance_id IS NULL AND session_id NOT IN (SELECT name FROM chat_sessions WHERE workspace_id=?)''', (ws,ws)):
    print(' store-only:', r)
