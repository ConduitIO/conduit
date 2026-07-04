# Conduit Governance

This document describes how the Conduit project is governed: how decisions get made, how
contributors become maintainers, and the commitments the project makes to everyone who builds on
it. It is intentionally lightweight and will grow as the community does.

## License commitment

Conduit is licensed under **Apache-2.0**, and the open-source project stays that way — forever.

- The engine, all connectors and processors, all SDKs, the registry and templates, the CLI, the
  built-in UI, the Helm chart, and the Kubernetes operator are Apache-2.0.
- **The one-way ratchet:** nothing shipped as open source is ever moved behind a paywall.
  Commercial features may become open source over time; the reverse never happens.
- Commercial products (org-scale governance and federation) live in a separate repository, above
  the open source, never inside it. The line is stated publicly in `ROADMAP.md`.

These are governance commitments, not marketing. A change to any of them would require the
process described below and could not be made quietly.

## Principles

The project's technical and strategic principles are recorded in `ROADMAP.md` and made durable in
Architecture Decision Records under `docs/architecture-decision-records/`. Governance exists to
uphold those principles, not to relitigate them case by case. In particular, decisions already
recorded as ADRs (single-node engine, no bespoke DSL, WASM component model, local-state-only
processing, broker neutrality) are settled; changing one requires a superseding ADR, not an
issue thread.

## Roles

Contribution is a ladder. You move up it by showing sustained, high-quality judgment — not by
volume alone.

**Users** file issues, ask questions in [Discussions](https://github.com/ConduitIO/conduit/discussions)
and [Discord](https://discord.meroxa.com), and help others. Every role below starts here.

**Contributors** open pull requests. Anyone who lands a merged PR is a contributor. See
`CONTRIBUTING.md` for how to start; the scaffolding tooling is designed to make a first
meaningful PR a single-day effort.

**Reviewers** are trusted to review PRs in an area they know well. A maintainer nominates a
contributor as a reviewer after a track record of good reviews and merged work. Reviewers can
approve Tier 2 and Tier 3 changes (see the PR review process in `CLAUDE.md`).

**Maintainers** own the project's direction and have merge rights. They are responsible for
release quality, the data-integrity invariants, and the review tiers. Maintainers are named
owners of the regressions they approve — review is a real job here, not a rubber stamp.

### Becoming a maintainer

A reviewer becomes a maintainer by nomination from an existing maintainer and lazy consensus
(see below) among the current maintainers. The bar is judgment under uncertainty: has this person
shown they will protect the invariants and say no to the wrong change, not just ship the right
one? Maintainers are listed in `MAINTAINERS.md` (added as the group grows beyond its founding
state).

Maintainers who become inactive for an extended period move to emeritus status by their own
request or by consensus of the active maintainers. This is a recognition of contribution, not a
demotion.

## Decision process

Most decisions are made through the normal PR and review process. For anything larger, the
project uses **lazy consensus**: a proposal is made in the open (an issue, a design doc in
`docs/design-documents/`, or an ADR), and if no maintainer objects within a reasonable review
window, it is accepted. Silence is assent; objection starts a discussion.

- **Small changes** (bug fixes, docs, chores, connectors): the PR review process in `CLAUDE.md`.
- **Significant changes** (data path, public contracts, new subsystems): a design doc or ADR
  first, then lazy consensus among maintainers. Data-path changes always require explicit human
  maintainer sign-off — they never merge on automated review alone.
- **Direction and governance changes** (roadmap phases, this document, the license line):
  proposed in the open, decided by consensus of the maintainers, and recorded so the reasoning
  survives.

When maintainers disagree and consensus cannot be reached, the change does not proceed until it
can. The project biases toward _not_ shipping a contested change to infrastructure people bet
their data on.

## Transparency

- Direction lives in `ROADMAP.md` and the public GitHub Project board.
- Decisions of consequence are recorded as ADRs — immutable once merged, superseded rather than
  edited, so future contributors can reconstruct _why_ without archaeology.
- Releases follow a monthly train with changelogs generated from conventional commits.
- Community happens in the open: [Discussions](https://github.com/ConduitIO/conduit/discussions),
  [Discord](https://discord.meroxa.com), and a monthly community call on a public calendar.

## Code of Conduct

Conduit has adopted the [Contributor Covenant](https://www.contributor-covenant.org/) as its
[Code of Conduct](https://github.com/ConduitIO/.github/blob/main/CODE_OF_CONDUCT.md). All
participation is subject to it.

## Security

Security vulnerabilities should be reported privately, not in public issues. The disclosure
process is documented in `SECURITY.md`.

## Changing this document

This document is itself governed by the process it describes: propose a change in the open, reach
consensus among maintainers, record the outcome. It is expected to evolve as the project grows
from its founding state toward a broader maintainer group.
