# Conduit Processor Template

This is a template project for building [Conduit](https://conduit.io) processors in Go. It makes it possible to
start working on a Conduit processor in a matter of seconds.

> [!TIP]
> Are you looking to write a simple processor for your specific use case? In that case you could take the simpler
> approach - check out the
> [documentation](https://conduit.io/docs/developing/processors/building#using-sdknewprocesorfunc) how you
> can write a processor in a single function!

## Quick start

1. Click [_Use this template_](https://github.com/new?template_name=conduit-processor-template&template_owner=ConduitIO) and clone your new repository.
2. Initialize the repository using [`setup.sh`](https://github.com/ConduitIO/conduit-processor-template/blob/main/setup.sh) and commit your changes.
   ```sh
   ./setup.sh github.com/myusername/conduit-processor-myprocessor
   git add -A
   git commit -m "initialize repository"
   ```
3. Set up [automatic Dependabot PR merges](#automatically-merging-dependabot-prs).

With that, you're all set up and ready to start working on your processor! As a next step, we recommend that you 
check out the [Conduit Processor SDK](https://github.com/ConduitIO/conduit-processor-sdk).

## What's included?

* Skeleton code for the processor and its configuration.
* Example unit tests.
* A [Makefile](/Makefile) with commonly used targets.
* A [GitHub workflow](/.github/workflows/test.yml) to build the code and run the tests.
* A [GitHub workflow](/.github/workflows/lint.yml) to run a pre-configured set of linters.
* A [GitHub workflow](/.github/workflows/release.yml) which automatically creates a release when a tag is pushed.
* A [Dependabot setup](/.github/dependabot.yml) which checks your dependencies for available updates and 
[merges minor version upgrades](/.github/workflows/dependabot-auto-merge-go.yml) automatically.
* [Issue](/.github/ISSUE_TEMPLATE) and [PR templates](/.github/pull_request_template.md).
* A [README template](/README_TEMPLATE.md).

## Automatically merging Dependabot PRs

> [!NOTE]
> This applies only to public processor repositories, as branch protection rules are not enforced in private repositories.

The template makes it simple to keep your processor up-to-date using automatic merging of
[Dependabot](https://github.com/dependabot) PRs. To make use of this setup, you need to adjust
some repository settings.

1. Navigate to Settings -> General and allow auto-merge of PRs.

   ![Allow auto-merge](https://github.com/user-attachments/assets/c1b6605a-866d-4bb6-b374-32328d83cd2d)

2. Navigate to Settings -> Branches and add a branch protection rule.

   ![Add branch protection rule](https://github.com/user-attachments/assets/dda83e9c-195b-40a0-87bb-7ae7dc8683ca)

3. Create a rule for branch `main` that requires status checks `build` and `golangci-lint`.

   ![Status checks](https://github.com/user-attachments/assets/bfc69fe8-8c3d-4f2a-a2c5-ae4395d7019f)

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
