# Conduit Connector Template

This is a template project for building [Conduit](https://conduit.io) connectors
in Go. It makes it possible to start working on a Conduit connector in a matter
of seconds.

## Quick start

1. Click [_Use this template_](https://github.com/new?template_name=conduit-connector-template&template_owner=ConduitIO) and clone your new repository.
2. Initialize the repository using [`setup.sh`](https://github.com/ConduitIO/conduit-connector-template/blob/main/setup.sh) and commit your changes.
   ```sh
   ./setup.sh github.com/myusername/conduit-connector-myconnector
   git add -A
   git commit -m "initialize repository"
   ```
3. Fill out the `summary` and `description` in `connector.yaml`. Note that the
   `source` and `destination` sections in this file shouldn't be changed, as
   they are automatically generated from the source and destination configuration
   structs.
4. Set up [automatic Dependabot PR merges](#automatically-merging-dependabot-prs).

With that, you're all set up and ready to start working on your connector! As a
next step, we recommend that you check out
the [Conduit Connector SDK](https://github.com/ConduitIO/conduit-connector-sdk).

## What's included?

* Skeleton code for the connector's configuration, source and destination.
* Example unit tests.
* A [Makefile](/Makefile) with commonly used targets.
* A [script](/scripts/bump_version.sh) that bumps the connector version.
* A [script](/scripts/tag.sh) that tags the connector (which kicks of a
  release).
* A [GitHub workflow](/.github/workflows/test.yml) to build the code and run the tests.
* A [GitHub workflow](/.github/workflows/lint.yml) to run a pre-configured set of linters.
* A [GitHub workflow](/.github/workflows/release.yml) which automatically
  creates a release when a tag is pushed.
* A [Dependabot setup](/.github/dependabot.yml) which checks your dependencies
  for available updates
  and [merges minor version upgrades](/.github/workflows/dependabot-auto-merge-go.yml)
  automatically.
* [Issue](/.github/ISSUE_TEMPLATE) and [PR templates](/.github/pull_request_template.md).
* A [README template](/README_TEMPLATE.md).

## Automatically merging Dependabot PRs

> [!NOTE]
> This applies only to public connector repositories, as branch protection rules are not enforced in private repositories.

The template makes it simple to keep your connector up-to-date using automatic
merging of [Dependabot](https://github.com/dependabot) PRs. To make use of this
setup, you need to adjust some repository settings.

1. Navigate to Settings -> General and allow auto-merge of PRs.

   ![Allow auto-merge](https://github.com/ConduitIO/conduit-connector-template/assets/8320753/695b15f0-85b4-49cb-966d-649e9bf03455)

2. Navigate to Settings -> Branches and add a branch protection rule.

   ![Add branch protection rule](https://github.com/ConduitIO/conduit-connector-template/assets/8320753/9f5a07bc-d141-42b9-9918-e8d9cc648482)

3. Create a rule for branch `main` that requires status checks `test` and
   `golangci-lint`.

   ![Status checks](https://github.com/ConduitIO/conduit-connector-template/assets/8320753/96219185-c329-432a-8623-9b4462015f32)

## Recommended repository settings

- Allow squash merging only.
- Always suggest updating pull request branches.
- Automatically delete head branches.
- Branch protection rules on branch `main` (only in public repositories):
  - Require a pull request before merging.
  - Require approvals.
  - Require status checks `build` and `golangci-lint`.
  - Require branches to be up to date before merging.
  - Require conversation resolution before merging.
  - Do not allow bypassing the above settings.
