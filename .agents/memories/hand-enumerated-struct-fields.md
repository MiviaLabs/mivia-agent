---
id: hand_enumerated_struct_fields
title: A struct field added without updating every hand-written enumeration of that struct is silent
content: When a struct's fields are walked by a hand-maintained list - a scaling slice, a preserving copy, a field-by-field bridge - a new field missed by one of them produces a wrong number rather than a compile error; gate it by reflecting over the struct, never by naming fields.
importance: high
tags: [[review, invariants, testing, reflection, gates, context-breakdown]]
---

# Hand-enumerated struct fields drift from the struct

## The pattern

A struct is walked by a list somebody wrote by hand:

- a `[]*int` of its own fields, to scale them so they sum to a total;
- a `countsOnly()`-style copy that keeps some fields and zeroes the rest;
- a field-by-field conversion to a same-shaped type in another package.

Adding a field is one edit. Keeping the enumerations correct is several,
in files the author has no reason to open, and **the compiler checks none
of them** - a missing entry is a shorter slice, not a type error.

## Why it is worth a memory

The failure has no symptom at the failure site. A field left out of a
scaling list keeps its raw magnitude while its siblings shrink, so the
displayed parts stop summing to the displayed whole. A field left out of a
preserving copy is zeroed on every pass, so a surface reads `0` forever.

`ContextBreakdown` (`internal/chat`, `internal/uikit/ports`) has five such
enumerations across three packages. An adversarial review of one added
field mutated each in turn: **eight mutations passed the whole test
suite**, three of them breaking the sum invariant outright.

## The trap that makes it survive review

The obvious test names the fields it knows about — and so shares the
enumeration's blind spot by construction. The test and the list are the
same list; the list is the bug.

## What to do instead

Gate over the **struct**, by reflection, and classify fields by
**behaviour** rather than by name or type:

- a field that moves `Total()` is a token cost, one that does not is a count;
- a cost that moves `Floor()` belongs to the floor, otherwise to the
  conversation.

Then assert every field is accounted for by every enumeration. A new field
joins the table by existing, not by being remembered. See
`internal/uikit/ports/breakdown_enumeration_test.go` and
`internal/chat/breakdown_enumeration_test.go`; class DC-39; INV-TUI-30.

Related: [[sibling-implementations-drift]] — same shape one level up, where
the drifting copies are implementations of an interface rather than
enumerations of a struct.
