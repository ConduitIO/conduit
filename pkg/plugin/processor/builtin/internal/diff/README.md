# Diff

This package contains code taken from https://github.com/golang/tools/tree/master/internal/diff
on February 15th, 2024. We need the code to create a unified diff between two strings.

The code is left as-is, except two changes:

- The imports were changed to reference the Conduit module path. This was done
  using the following command:

  ```sh
  find . -type f -exec sed -i '' 's/golang.org\/x\/tools\/internal/github.com\/conduitio\/conduit\/pkg\/plugin\/processor\/builtin\/internal/g' {} +
  ```

- The package `golang.org/x/tools/internal/testenv` was added into the `diff` package,
  as that's the only place it's used. It also only includes the required functions.
