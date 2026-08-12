package agent

import (
	"fmt"
	"slices"
)

// refOnlyTier implements the ref-only tier (plan tools/06): a tool
// explicitly opted out of inlining never reaches the budget tiers at all.
// When its RAW body clears the floor and the result is not ephemeral, the
// WHOLE body is spooled and the result is replaced by a notice naming the
// remainder - only the notice's bytes are charged, exactly as tier 3 charges
// its notice alone. No ref is ever invented (INV-AG-10): a nil spool, an
// empty principal, or a failed store yields the plain notice.
func refOnlyTier(env shapeEnv, p resultParts, name string) (string, bool) {
	if !slices.Contains(env.refOnlyTools, name) ||
		p.totalN < BatchDegradeFloorBytes || p.ephemeral {
		return "", false
	}
	ref := env.spool(p.cappedBody)
	var notice string
	if ref != "" {
		notice = fmt.Sprintf("[tool result for %s elided to a remainder ref (original ~%s): %s — use read_output to fetch the full body]",
			name, sizeBucketLabel(p.totalN), ref)
	} else {
		notice = fmt.Sprintf("[tool result for %s elided; original ~%s]",
			name, sizeBucketLabel(p.totalN))
	}
	return notice, true
}

// sizeBucketLabel rounds n up to the next power of two and renders it as
// KiB or MiB (powers of 1024), the same label contextmgr's elision notices
// carry (plan tools/06): the ref-only notice has to agree with the rest of
// the product. The ref-only tier only calls it with n >=
// BatchDegradeFloorBytes, so the sub-KiB corner never arises here.
func sizeBucketLabel(n int) string {
	if n <= 0 {
		return "0 KiB"
	}
	bucket := ceilPowerOfTwo(n)
	const (
		kib = 1024
		mib = 1024 * 1024
	)
	if bucket >= mib {
		return fmt.Sprintf("%d MiB", bucket/mib)
	}
	if bucket < kib {
		return "1 KiB"
	}
	return fmt.Sprintf("%d KiB", bucket/kib)
}

// ceilPowerOfTwo rounds n up to the smallest power of two >= n, saturating at
// the largest representable power of two (1<<(intSize-2)) so a doubling can
// never overflow int. Mirrors contextmgr's helper of the same name.
func ceilPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	maxInt := int(^uint(0) >> 1)
	p := 1
	for p < n {
		if p > maxInt>>1 {
			return p
		}
		p <<= 1
	}
	return p
}
