# Contributing to Ikhnos

Thank you for looking. Issues, questions and pull requests are all welcome. This is the short version; the [developer guide](https://alexandrosst.github.io/ikhnos/contributing/developer-guide) (sources in [`docs-site/docs/contributing`](docs-site/docs/contributing/developer-guide.md)) covers building, running an agent without a cluster, and where things live.

## Before you start

- **Small fix or clear bug:** open a pull request directly.
- **New feature, or anything that changes behaviour:** open an issue first so we can agree on the shape. Ikhnos has a few deliberate rules that a change must not break, and it is cheaper to find that out before the code is written.
- **Not sure it is a bug:** ask in an issue. Say what you ran, what you saw and what you expected.

## The rules a change has to respect

- **Read-only.** Nothing in the agent writes to a cluster, and nothing in Ikhnos deploys or moves anything without a person approving it.
- **The server only narrows trust.** What an agent reports can be reduced by the server, never widened past what the cluster's own Helm install allows.
- **Say when you cannot tell.** Advice has three answers, *fits*, *does not fit* and *can't tell*. A missing fact is named in the answer; it is never filled with a guess that looks like a measurement.
- **One writer.** The server holds one SQLite writer. Do not add a second.
- **Layers are enforced by tests.** Go packages and the frontend's feature folders have import rules (for example, shared code never imports a feature, and nothing imports the app shell). The architecture tests fail with the rule they enforce; do not edit them to make a change pass.

## Running the checks

```bash
# frontend
npm install
npm run lint
npm run test:unit
npm run test:ui
npm run build            # type-check + production build

# backend
cd backend
go vet ./...
go test -race ./...
```

`npm run test:e2e` runs the browser tests against a built `dist/`. CI runs all of the above on every pull request; a pull request should be green before review.

## Pull requests

- Keep one concern per pull request, and say *why* in the description, not only what.
- Add or update a test for the behaviour you changed. A bug fix starts with a test that fails.
- If you change a JSON shape the UI reads, regenerate the frontend's wire types (`go test ./internal/server -run TestWire -update`, from `backend/`); a test fails when the committed file and the Go types disagree. The FUSION API's OpenAPI document and reference page are generated from its operations in `backend/internal/fusionhttp`, so change them there.
- If you change what a user sees or can configure, update the page for it in [`docs-site/docs`](docs-site/docs). The docs build fails on a broken link.
- Commit messages say what changed and why, in the imperative or the plain past, as a sentence.

## Reporting a security problem

Not here: see [SECURITY.md](SECURITY.md).

## License

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE), the license of the project.
