# Contributing

Issues and pull requests are welcome.

- garbaj deletes files unattended, so every change to what counts as safe needs a
  test that proves the unsafe case is still kept.
- Standard library only; please do not add dependencies.
- Before opening a pull request, run:

```bash
gofmt -l . && go vet ./... && go test ./...
```
