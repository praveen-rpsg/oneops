# Contributing to OneOps

## Workflow

- **Trunk-based.** Branch from `main` as `feat/<short>`, `fix/<short>`, `chore/<short>`; keep branches < 2 days.
- **Conventional commits** (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`) — drives changelog/versioning.
- Open a PR; CI must be green (build, test, vet, lint, security, conformance).

## Definition of Done

- [ ] Code compiles (`make build`) and is formatted (`make fmt`).
- [ ] `make vet` and `make lint` clean.
- [ ] `make test` green with the race detector; new logic has tests (≥80% on changed packages).
- [ ] No `TODO`/placeholder/mocked production code paths.
- [ ] Public functions and packages documented.
- [ ] Observability preserved (structured logs; metrics/traces added when the dependency lands).

## Local setup

```bash
cp .env.example .env
make up        # start dependencies
make test      # run the suite
make run       # start the control plane
```

## Code style

- Go: standard `gofmt` + `goimports`; small packages; errors wrapped with context; no panics in library code.
- Prefer the standard library; add a dependency only when it removes real work.
- Tests: table-driven; `httptest` for handlers; `testcontainers` for integration (from M1).

## Releasing

Tags are `vMAJOR.MINOR.PATCH`. Images are built by CI and tagged `sha` + semver;
promotion to environments is GitOps (ArgoCD).
