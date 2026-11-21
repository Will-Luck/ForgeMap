# Contributing

Issues and ideas are welcome. Use the issue forms: a
[bug report](https://github.com/Will-Luck/forgemap/issues/new?template=bug_report.yml)
or a
[feature request](https://github.com/Will-Luck/forgemap/issues/new?template=feature_request.yml).
For a vulnerability, see [SECURITY.md](SECURITY.md) instead.

## Running it and the tests

You need Go. From the repository root:

```bash
make build     # builds ./forgemap
make demo      # runs it against the built-in fake forge, no token or network
make fixture   # serves testdata/site.json with no network, for web work
make test      # go test -race ./...
make check     # gofmt, go vet and the tests: run this before a pull request
make image     # builds the Docker image
```

## How changes are taken

This repository is published from a separate working tree, one commit per
release. A pull request is therefore not merged with the merge button. Its
change is applied by hand, credited in the changelog, and the pull request is
closed when the release with it is out.

Small, focused pull requests with a test are easiest to take.
