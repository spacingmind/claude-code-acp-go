---
name: adr
description: Create a new architecture decision record in docs/decisions/, following the existing numbered format. Use when a material architectural choice (per AGENTS.md rule d) has been decided and needs to be recorded, or when asked to "write an ADR" or "record this decision".
---

Only write an ADR for a decision that's actually been made (with the user,
per AGENTS.md rule d — don't record a decision the user hasn't confirmed).

1. Find the next number: `ls docs/decisions/ | sort | tail -1` and increment
   (zero-padded to 4 digits, e.g. `0001` → `0002` → `0003`).
2. Create `docs/decisions/NNNN-<slug>.md` (kebab-case slug describing the
   decision) with this structure, matching `docs/decisions/0001-*.md` and
   `0002-*.md`:

```markdown
# NNNN: <Title>

## Status

Accepted

## Decision

<What was decided, stated plainly.>

## Alternatives considered

- **<Alternative A>.** <Why it was passed over.>
- **<Alternative B>.** <Why it was passed over.>

## Rationale

<Why this decision, in terms of the actual constraints/goals that drove it.>
```

3. Keep it short — an ADR records the decision and its reasoning, not a full
   design doc. If the decision affects `docs/ARCHITECTURE.md` or
   `docs/ROADMAP.md`, update those too and note the ADR link there.
4. If this decision came out of a `docs/plans/active/<slug>.md` spec (see
   the `plan` skill), link the ADR from that plan's Decisions section, and
   vice versa — an ADR records *what* was decided and why it's the right
   call in general; the plan records *how* that decision applies to the
   specific work it's part of. Keep them cross-referenced rather than
   duplicating one into the other.
