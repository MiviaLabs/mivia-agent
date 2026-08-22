# Method fault catalog

Fault classes for the function pass. Check each class against the
function under review. Each item names the fault and the signal that
exposes it.

## Condition faults

- Inverted comparison: `>` where `>=` belongs. Trace the boundary
  value itself through the condition.
- Duplicated condition: an else-if chain that tests the same
  condition twice. The second arm is dead.
- Always-true or always-false condition: a dead branch. Test it or
  delete it.
- Missing switch default: an unhandled value falls through silent.
  A type switch with no default hides new cases.
- Operator fault: assignment where comparison belongs. Go forbids
  this in conditions, but string builders and flag variables still
  carry it.

## Boundary faults

- Off-by-one loop bound: `<=` where `<` belongs. Trace the last
  index.
- Slice cut off by one: `s[1:]` where `s[0:]` belongs, or
  `len(s)-1` applied twice.
- Empty range: the loop body never runs. The result must still be
  correct for the empty input.
- First or last element handled wrong: the loop covers the interior
  and misses an end.
- Zero-length result: a valid outcome, not an error. Check the
  function does not turn it into one.

## Aliasing faults

- Append sharing: append on a slice whose backing array another
  holder retains. One write becomes visible to both.
- Slice header reassignment: assigning to the slice variable does
  not mutate the elements. Mutate an element to test sharing.
- Range copy: the loop variable is a copy. In Go before 1.22, a
  closure or goroutine that captures it sees the last value.
- Receiver by value: a method that must mutate state but takes a
  value receiver mutates a copy.
- Map retention: a map handed out stays shared. Copy it when the
  caller must not see later writes.

## Nil and zero-value faults

- Nil map write: a write to a nil map panics. Initialize it.
- Nil pointer method call: legal when the method handles a nil
  receiver. Check the method does.
- Dereference after error: a path returns an error and the caller
  still reads the result.
- Zero struct after failed parse: a failed decode leaves zero
  values that look valid. Check the error path clears them.
- Empty string versus nil pointer: different wire meanings. Check
  the function does not conflate them.

## Error faults

- Shadowed error: `:=` inside a scope declares a new error variable.
  The outer check reads the old one.
- Unchecked error: a returned error ignored. Vet catches the plain
  form; assigned-but-unused forms need the eye.
- Result used after error: the contract says the result is invalid
  on error. Check the callers honor it.
- Broken sentinel: wrapping without `%w` breaks `errors.Is`. The
  caller can no longer match the error.
- Message without the operand: the error text omits the value that
  caused the failure. Debugging goes blind.

## Conversion faults

- Narrowing conversion: int to a smaller integer type truncates.
- Negative to unsigned: the value wraps to a huge number.
- Truncating division: integer division drops the fraction toward
  zero. Compute in float when the fraction matters.
- Byte versus rune: byte indexing on multibyte text splits a
  character. Use rune or decode iteration.

## Concurrency faults

- Lock copied by value: a mutex receiver must be a pointer. A value
  receiver copies the lock.
- Read path without the lock: reads of shared state need the same
  lock discipline as writes.
- WaitGroup Add inside the goroutine: the Wait can pass before the
  Add runs. Add before the go statement.
- Shared map write: maps are not safe for concurrent writes. Guard
  them or use a channel.
- Test helper on another goroutine: testing.T methods are not safe
  from other goroutines. Channel the result back and assert on the
  test goroutine.

## Invariant faults

- Rule only in a comment: the repo requires every stated rule in a
  Validate method. A comment rule without enforcement is a finding.
- Validate misses a dimension: duplicate identifiers, self
  references, and unknown keys each need a check.
- Forbidden transition allowed: the status model pins the legal
  transitions. Check Fire against that table, not against the
  caller's assumption.

## Sources

- Michaela Greiler, code review checklist: logic errors, edge
  cases, error handling, and test-case completeness questions.
- Axify and Codacy checklists: off-by-one errors, boundary inputs,
  and error-path review.
- Go wiki and Go blog: range-variable semantics and the loop
  variable change in Go 1.22.
