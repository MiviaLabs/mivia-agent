# Sweep greps must not filter the raw pattern

When a commit's Sweep trailer counts sites of a defect class, run the raw
mechanism grep and disposition EVERY hit by hand. Do not narrow the grep
with identifier-prefix filters (`grep -v "Default|max"` and similar) to
shrink the list first.

**Why:** During the DC-7 timeout-saturation sweep (commits 938f4784,
779378e0, aeca04d3), a prefix filter meant to drop compile-time constants
also hid three live sibling sites - one of them the third copy of the
exact `ToolRunTimeoutSec` wiring the sweep existed to fix. Two commits
shipped with a wrong "found 0 further" count before the raw grep exposed
them. A filter encodes the assumption "these hits are safe" without
looking at them, which is the same failure the sweep discipline exists to
prevent.

**How to apply:** Run the bare pattern over non-test code, list every
hit, and classify each one explicitly (constant / clamped upstream /
range-checked at parse / unguarded). The Sweep trailer's count must come
from that full list. Related: [[transport-stage-timeout-is-not-a-deadline]],
[[sibling-implementations-drift]].
