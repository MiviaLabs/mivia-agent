package storage

import (
	"net/url"
	"strings"
)

// pragmaDSNParams are the driver-side pragma overrides applied by
// modernc.org/sqlite's applyQueryParams to every pooled connection. OpenSQLite
// also executes the same PRAGMAs explicitly after open, so this DSN form is
// per-connection parity, not the only enforcement point.
const pragmaDSNParams = "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

const pragmaReadOnlyDSNParams = "_pragma=query_only(1)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

// writeTxDSNParams add _txlock=immediate to the shared pragmas. The driver
// emits BEGIN IMMEDIATE for every BeginTx on such a connection, so the
// transaction takes SQLite's single write lock before its first read instead
// of upgrading a read lock at its first write.
//
// The upgrade is what fails: when another process commits between a deferred
// transaction's read and its write, the upgrade fails with
// SQLITE_BUSY_SNAPSHOT, which busy_timeout cannot clear because the snapshot
// is already stale. Under BEGIN IMMEDIATE the second writer blocks on the
// write lock and busy_timeout does apply, so both commits land.
//
// This is deliberately NOT the default DSN. The worktree fence uses the
// deferred upgrade failure as an optimistic-concurrency conflict detector -
// see beginWrite.
const writeTxDSNParams = pragmaDSNParams + "&_txlock=immediate"

// sqliteDSN builds the driver DSN for a store file. The modernc.org/sqlite
// driver (v1.54.0) splits a DSN at the first literal '?' to separate the
// filename from its query parameters, so a POSIX filename containing '?'
// ("ledger?part.db") would be silently truncated to "ledger" and the store
// would open the wrong database file. Paths without '?' keep the historical
// path+"?"+pragmas form byte-for-byte; paths with '?' are percent-escaped
// into a file: URI whose only literal '?' is the query separator, and SQLite's
// URI decoder (SQLITE_OPEN_URI) restores the literal '?' (and '/' for absolute
// paths) in the real filename. Escaping the whole path also prevents URI
// authority ("//"), query ('?'), and fragment ('#') injection into the path
// portion, so no wrong-file or path-confusion vector remains.
func sqliteDSN(path string) string { return storeDSN(path, pragmaDSNParams) }

func sqliteReadOnlyDSN(path string) string { return storeDSN(path, pragmaReadOnlyDSNParams) }

// sqliteWriteDSN builds the driver DSN for the immediate-txlock write pool.
// It applies the identical path transform as sqliteDSN; only the parameters
// differ. See writeTxDSNParams.
func sqliteWriteDSN(path string) string { return storeDSN(path, writeTxDSNParams) }

func storeDSN(path, params string) string {
	if strings.Contains(path, "?") {
		return "file:" + url.PathEscape(path) + "?" + params
	}
	return path + "?" + params
}
