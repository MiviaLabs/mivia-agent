# ADR 0003: Docs Ownership

## Status

Accepted

## Context

Agents frequently rewrite the same policy into multiple markdown files, causing drift.

## Decision

- `docs/OWNERS.yaml` assigns each topic exactly one path
- `scripts/check_docs_ownership.py` fails on missing ownership, missing files, and duplicate H1 titles
- Forbidden parallel roots for free-form wiki-style dumps
- ADRs are numbered and allowlisted as self-owned decisions

## Consequences

- Docs changes are force-routed to canonical paths
- Slight friction when adding new topics (must update OWNERS in the same change)
- Higher long-term reliability of agent-maintained documentation
