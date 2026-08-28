---
name: plan
description: Create or update a docs/plans/active/<slug>.md spec — acceptance criteria and test scenarios written before implementation starts, per AGENTS.md rule (c) and this repo's spec-driven-development practice. Use when starting work that will span more than one session, when spawning an implementation (sub-)agent for anything non-trivial, or when asked to "make a plan", "write a spec", "track this work", or "write a plan doc".
---

This repo follows spec-driven development: for anything beyond a trivial
change, the spec — acceptance criteria and concrete test scenarios — gets
written down *before* implementation starts, not inferred from the code
afterward. This is what keeps an implementing agent from writing a test that
just confirms whatever it happened to build, and what keeps a review pass
grounded in what was actually asked for rather than what shipped.

Multi-session work (and any non-trivial single-session implementation task,
especially one handed to a sub-agent) gets tracked in
`docs/plans/active/<slug>.md`, not just in conversation.

## Creating a new plan

1. Pick `<slug>` — short kebab-case, matching the work (e.g.
   `phase-1-routing-engine`, `accounts-registry`).
2. Write `docs/plans/active/<slug>.md` with these sections, in this order —
   Acceptance Criteria and Test Scenarios come first because they're the
   spec; everything after is how that spec got satisfied:

```markdown
# <Title>

## Acceptance Criteria

Concrete, checkable statements of what "done" means. Not vague ("handles
errors well") — specific enough that someone could grade the finished work
against this list without asking you anything else ("returns 404 for a
missing workspace id", "session affinity holds across two calls with the
same key", "no accounts configured returns a provider-shaped 503, not a
generic error").

## Test Scenarios

The specific cases the implementation must be tested against, named up
front — not "add tests," but the actual scenarios: the happy path, the
edge cases you already know matter (empty input, missing id, concurrent
calls, cancellation), and any failure mode worth locking in a regression
test for. Naming these before implementation exists is the point: it's
much harder to quietly skip a hard case if it was written down first.

## Decisions

Architectural or scope decisions made for this work, and why. Link to
docs/decisions/ ADRs where one was written.

## Progress

Running log of what's done, in progress, and not started. Update this as
work proceeds — don't let it go stale mid-session.

## Validation

How this was/will be verified: commands run, manual checks. Map back to
Acceptance Criteria explicitly — for each one, note how it was confirmed
(which test, which manual check) — not just "tests pass."
```

3. Keep the file updated as work proceeds — this is a living doc, not a
   one-time snapshot. If you're about to spawn a sub-agent to implement this
   work, its task prompt should be grounded in this file's Acceptance
   Criteria and Test Scenarios, not a fresh ad-hoc restatement of them.

## Finishing a plan

When the work is done and validated — every Acceptance Criterion has a
corresponding line in Validation, `git mv` the file from
`docs/plans/active/<slug>.md` to `docs/plans/completed/<slug>.md`, with the
Progress and Validation sections reflecting the final state.
