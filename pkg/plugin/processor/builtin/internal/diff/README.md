# Diff

This package contains code taken from <https://github.com/golang/tools/tree/master/internal/diff>
on March 31st, 2025 (commit SHA: d68fc51f28b0d6ea8e4fa70418d7eb8c475c6257). We
need the code to create a unified diff between two strings.

The code is left as-is, except 3 changes:

- The imports were changed to reference the Conduit module path. This was done
  using the following command:

  ```sh
  find . -type f -exec sed -i '' 's/golang.org\/x\/tools\/internal/github.com\/conduitio\/conduit\/pkg\/plugin\/processor\/builtin\/internal/g' {} +
  ```

- The package `golang.org/x/tools/internal/diff/myers` was removed, as it's deprecated.

- The package `golang.org/x/tools/internal/testenv` was added into the `diff` package,
  as that's the only place it's used. It also only includes the required functions.

## Developing on MacOS

Note that the code expects GNU diff to be installed on the system. If you're
using a Mac, you can install it using Homebrew:

```sh
brew install diffutils
```