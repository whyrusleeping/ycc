# Build and test (verification runbook)

A repeatable verification procedure for the ycc repo. Run it to confirm the tree
builds cleanly and all tests pass — e.g. after implementing a task, before committing.

## Steps

1. Build every package:

   ```
   go build ./...
   ```

2. Run the vet checks:

   ```
   go vet ./...
   ```

3. Run the full test suite:

   ```
   go test ./...
   ```

## Expected outcome

All three commands exit 0: the build succeeds, `go vet` reports nothing, and every
package's tests pass (`ok` / no `FAIL`). Report any command that fails with its output.
