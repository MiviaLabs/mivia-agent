package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Lease-holder liveness. RenewLease stamps an opaque holder token next to
// lease_at; ReclaimSession may take over a FRESH lease when that token
// proves the recorded holder is dead. Anything short of proof - a different
// host, a different boot, an unreadable /proc, a token this version does
// not understand - falls back to the pure-TTL wait, so an uncertain holder
// is never evicted (DC-2: the fence fails closed).

// leaseHolderVersion prefixes every token so a future format change makes
// old tokens fall back to TTL instead of being misparsed.
const leaseHolderVersion = "v1"

// Seams for tests; production uses the real host.
var (
	leaseHostname = os.Hostname
	leaseReadFile = os.ReadFile
	leaseGOOS     = runtime.GOOS
)

// currentLeaseHolder mints this process's holder token:
// version|host|boot_id|pid|starttime. An empty string (identity could not
// be established) stamps NULL semantics - readers then use TTL only.
func currentLeaseHolder() string {
	host, err := leaseHostname()
	if err != nil || host == "" {
		return ""
	}
	boot, err := leaseBootID()
	if err != nil || boot == "" {
		return ""
	}
	start, err := leaseProcStartTicks(os.Getpid())
	if err != nil {
		return ""
	}
	return strings.Join([]string{leaseHolderVersion, host, boot, strconv.Itoa(os.Getpid()), start}, "|")
}

// leaseBootID reads the kernel boot id, which changes every reboot and so
// guards pid-reuse across reboots.
func leaseBootID() (string, error) {
	b, err := leaseReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), err
}

// leaseProcStartTicks returns field 22 of /proc/<pid>/stat (starttime,
// clock ticks since boot) - stable for a process's lifetime and different
// for a reused pid. comm (field 2) may itself contain spaces and
// parentheses, so fields are counted after the LAST ')'.
func leaseProcStartTicks(pid int) (string, error) {
	b, err := leaseReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", fmt.Errorf("malformed stat for pid %d", pid)
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is state (field 3 of the file); starttime is file field 22.
	if len(fields) < 20 {
		return "", fmt.Errorf("short stat for pid %d", pid)
	}
	return fields[19], nil
}

// leaseHolderDead reports PROOF that the token's holder no longer runs:
// same host, same boot, and the pid is gone or was reused (starttime
// differs). Every other outcome - including any read error other than the
// pid's /proc entry being absent - is "not provably dead".
func leaseHolderDead(token string) bool {
	if leaseGOOS != "linux" {
		return false
	}
	parts := strings.Split(token, "|")
	if len(parts) != 5 || parts[0] != leaseHolderVersion {
		return false
	}
	host, err := leaseHostname()
	if err != nil || host != parts[1] {
		return false
	}
	boot, err := leaseBootID()
	if err != nil || boot != parts[2] {
		return false
	}
	pid, err := strconv.Atoi(parts[3])
	if err != nil || pid <= 0 {
		return false
	}
	start, err := leaseProcStartTicks(pid)
	if err != nil {
		// Absent /proc entry on the same host and boot: the pid is gone.
		return errors.Is(err, fs.ErrNotExist)
	}
	return start != parts[4]
}
