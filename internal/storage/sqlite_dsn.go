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
func sqliteDSN(path string) string {
	if strings.Contains(path, "?") {
		return "file:" + url.PathEscape(path) + "?" + pragmaDSNParams
	}
	return path + "?" + pragmaDSNParams
}
