### Description

Please include a summary of the change and what type of change it is (new feature, bug fix, refactoring, documentation).
Please also include relevant motivation and context.
List any dependencies that are required for this change.

Fixes # (issue)

**Roadmap item:** <!-- the ROADMAP.md item this advances -->

**Risk tier:** <!-- Tier 1 (data path) | Tier 2 (feature) | Tier 3 (chore). Required. See CLAUDE.md, "PR review process". -->

### Quick checks

- [ ] I have followed the [Code Guidelines](https://github.com/ConduitIO/conduit/blob/main/docs/code_guidelines.md).
- [ ] There is no other [pull request](https://github.com/ConduitIO/conduit/pulls) for the same update/change.
- [ ] I have written unit tests.
- [ ] I have made sure that the PR is of reasonable size and can be easily reviewed.
- [ ] The diff against `main` contains only the files and lines I meant to change (check `go.mod` and `go.sum`: a module version that goes down is a revert).

### Tier 1 only (record flow, ack/position/checkpoint, state, connector protocol, serialization)

Delete this section for Tier 2 and Tier 3. A Tier 1 PR with an empty field here is not ready for review.

- **Design doc / ADR:** <!-- link, or a stated waiver for a small fix -->
- **Failure-mode analysis:** <!-- what could this break, which metric or alert would show it, how do we roll back -->
- **Existing behaviour on the touched path:** <!-- list the callers, defaults and fail-open branches already there and what each does with this change; reading only the diff is not enough -->
- **External-system premises, verified:** <!-- each assumption about a database, broker, pooler or cloud service, with version, command and output; "none" if there are none -->
- **Fail-open decisions:** <!-- every point where the code continues when it cannot tell whether that is safe, and why it is not silent data loss; "none" if there are none -->
- **Guards marked `// guard:` and killed by the mutation gate:** <!-- names, or "none added" -->
- **Chaos and upgrade tests:** <!-- updated, or why unaffected -->
- **Known gaps:** <!-- anything that can still lose, duplicate or change a record. "Documented limitation" is not an answer for data loss; fix it, fail closed, or say it blocks the release -->
- **Adversarial self-review (AI-authored code):** <!-- findings and resolutions -->

The review verdict is posted on this PR by a separate session, as `Review: approve @ <head sha>` or
`Review: changes-needed @ <head sha>`. DeVaris's sign-off is `Sign-off @ <head sha>`. A push makes both stale.
