# AI Agent Configuration

## Development

* Use `make imports` before committing any change to Go code.
* Use `go mod tidy` after any change to a `go.mod`.
* Run `make lint` and fix all reported issues before submitting a pull request.
* Run the e2e tests before submitting a pull request.
* When possible, try to keep pull requests small and self-contained to make reviews easier. Follow
  the repository's `.github/pull_request_template.md` and make sure to focus more on the reasons,
  background and encountered problems that motivated the change and less on reiterating code
  changes.

## Documentation

* Lines in Markdown files should not exceed 100 characters (use explicit line breaks).

## Testing Instructions

* Use `make clean build test-e2e` for running e2e tests.
* To run a specific test package, use the `WHAT` environment variable, like
  `WHAT=./test/e2e/apiexport make test-e2e`, or the `TEST_FLAGS` variable like
  `TEST_FLAGS="-v -run NameOfTheTestFunction" make test-e2e`.

## Changelogs

* When generating a changelog for a new release, group all relevant pull requests based on their
  `kind/...` label. Output these groups in descending order of importance.
* Only include pull requests in the changelog that have a `release-note` block in their descriptions
  on GitHub that is not empty or `NONE`.
* List each pull request in the following form: `#<number>: <release note> (by @<author>)`
