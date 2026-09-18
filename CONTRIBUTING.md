# Contributing

## Development

Requirements:

- Go 1.23+
- Windows 10/11 for Windows-specific runtime tests

Run the portable test suite:

```bash
go test ./... -count=1
go vet ./...
```

Compile Windows-specific tests from another OS:

```bash
GOOS=windows GOARCH=amd64 go test -c .
```

Build:

```bash
GOOS=windows GOARCH=amd64 go build -trimpath -o codex-route-guard.exe .
```

## Adapter policy

New adapters must identify an authoritative ownership source. Do not add broad pattern matching that turns unknown config into owned config.

A compatibility rule should document:

1. what component owns the state;
2. how ownership is proven;
3. what fields may be reconciled;
4. what conflicts must fail closed;
5. whether a lifecycle API exists;
6. how startup races are handled.

## Pull requests

Changes to routing, ownership, or process recovery should include regression tests. Windows behavior must pass the Windows CI job before merge.
