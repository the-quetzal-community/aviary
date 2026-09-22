# Hi Claude
Use goimports to deal with go imports.
Use `gd` command instead of the `go` command, use `gd vet` as the build validation.
Use `gd doc` for getting symbol documentation.
Musical format must remain backwards compatible!
All mutations must be observable by all clients!

# Verification ladder (run in this order before committing)
1. `gd vet` — build validation.
2. `go test ./internal/...` — unit tests for the pure-Go packages (use `go test`, not `gd test`, for these).
3. `go test -race ./internal/nettest/` — the co-op mutation gate (`TestMutationGate`): every musical mutation kind must fan out to all peers, persist iff committed, catch up a late joiner, and survive a host reload. **Any new mutation kind or persisted field needs a row in the `mutations` table** in `internal/nettest/gate_test.go`.
4. `tools/devshot.sh` — headless in-engine scenarios (`internal/devdemos.go`): boot, editor switching, scenery place/undo/redo, terrain raise/undo/redo in a scratch profile. `tools/devshot.sh --shot <name>` runs one windowed and saves PNGs to /tmp/aviary-devshot for visual checks; `AVIARY_DEMO=<name>` / `AVIARY_SHOT=<png>` drive a single run. New editor features should add a scenario.
5. Manual play-test for anything about feel.
