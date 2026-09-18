# Codex Route Guard

[中文](README.zh-CN.md) · [Architecture](ARCHITECTURE.md) · [Security](SECURITY.md) · [Compatibility](COMPATIBILITY.md)

> An ownership-aware route and configuration reconciliation layer for Codex integrations on Windows.

Codex Route Guard addresses a problem that appears when multiple independent tools share the same Codex configuration surface.

Account switchers, local API bridges, custom model routers, model-catalog injectors, launchers, and native Codex can all modify the same `~/.codex/config.toml`. Without an ownership protocol, the result is **last-writer-wins**: authentication may switch successfully while a route disappears, a local bridge may remain healthy while Codex silently falls back to native, a model catalog can be replaced by another tool, or Codex can start milliseconds before the intended route is restored.

Codex Route Guard is not another router. It is a small user-space **reconciliation layer** that keeps known ownership and lifecycle state coherent while refusing to take over configuration it cannot prove it owns.

## Why this exists

A typical multi-tool setup looks like this:

```text
Account / profile manager       Local route provider
          |                             |
          | writes auth/provider        | owns Responses route
          v                             v
                 ~/.codex/config.toml
                          |
                          v
                       Codex
```

The file is a shared control plane, but the tools are not transacting together.

That creates several classes of bugs:

- **route ownership drift** — the local route is healthy, but another tool removes `openai_base_url`;
- **model catalog interference** — one tool injects a catalog while another expects native or routed catalog state;
- **lifecycle mismatch** — manually restoring one field leaves the route provider's journal or restore state inconsistent;
- **startup races** — the final file is correct, but Codex already cached the wrong startup state;
- **unsafe repair loops** — two watchers repeatedly overwrite each other.

The Guard separates three concerns:

1. **Ownership** — who can prove that a field belongs to it?
2. **Lifecycle** — does the owner provide a safe connect/disconnect mechanism?
3. **Reconciliation** — what may be repaired without taking over unknown configuration?

The project rule is simple:

> **Reconcile only what you can prove you own; fail closed on everything else.**

## Current adapters

The architecture is intentionally broader than one compatibility bug, but the current implementation is conservative:

| Integration | Current role |
| --- | --- |
| `codex-chatgpt-web` | First-class route-owner adapter through its integration journal and route lifecycle |
| Cockpit Tools | Supported account/config mutation source, including known managed model catalogs |
| Native Codex | Native fallback state |
| Unknown custom routers/providers | Preserved; the Guard refuses takeover |
| Arbitrary third-party route journals | Not implemented yet |

So the Guard does **not** claim every Codex configuration. Unknown ownership is treated as a conflict, not permission to overwrite.

## What it does today

### Route ownership from authoritative state

For `codex-chatgpt-web`, the Guard reads the integration journal written by the route owner itself. It does not guess a port or hard-code a specific `127.0.0.1:<port>` value.

### Lifecycle-aware Web / Native switching

When possible, the Guard uses the route provider's own:

```text
route connect
route disconnect
```

That keeps the provider's journal, restore state, hooks, realtime assignment, and cache handling coherent.

### Narrow config repair

If an account/profile operation removes the already-owned Web route, the Guard can restore that route. It removes a model catalog only when the catalog is explicitly known to be managed by a supported mutation source and conflicts with the active routed state.

The Guard refuses to silently overwrite:

- an unknown `model_provider`;
- an unknown `model_catalog_json`;
- a different custom `openai_base_url`.

### Launch-race recovery

Sometimes configuration repair happens after Codex has already started.

The Windows recovery path correlates the config write time with **freshly-started** ChatGPT/Codex packaged processes, resolves one matching app family, and reloads only that fresh family. Older sessions are intentionally excluded.

### Independent process

The Guard runs outside Cockpit, Codex, and route-provider binaries. Ordinary updates to those applications do not overwrite the Guard executable.

## State model

Default mode is `auto`:

```text
Web route owner live
       |
       v
ensure route lifecycle is connected
       |
       +-- external mutation removes owned route --> reconcile
       |
       +-- Codex started during mutation ----------> fresh-process recovery only

route owner absent after grace period
       |
       v
disconnect through owner lifecycle
       |
       v
native Codex state
```

Manual modes:

```powershell
codex-route-guard.exe mode auto
codex-route-guard.exe mode web
codex-route-guard.exe mode native
```

## Install

Windows 10/11 is the supported target.

Download the Windows release/archive, extract it, then run:

```text
INSTALL.cmd
```

or:

```powershell
.\codex-route-guard.exe install
```

No administrator rights are required.

The installer uses a per-user location:

```text
%LOCALAPPDATA%\CodexRouteGuard\
```

If an older pre-rename **CodexWebGPTGuard** installation is present, the installer stops its background process, removes its autostart entry, and migrates the saved mode.

## Normal usage

With `auto` mode, you normally do not interact with the Guard.

For Web models:

```text
start Codex Web GPT -> switch/select account if needed -> launch Codex
```

For native Codex:

```text
close Codex Web GPT -> switch/select account if needed -> launch Codex
```

The Guard reconciles the route in the background.

## Status

```powershell
codex-route-guard.exe status
codex-route-guard.exe version
```

Logs:

```text
%LOCALAPPDATA%\CodexRouteGuard\guard.log
```

## Uninstall

```text
UNINSTALL.cmd
```

or:

```powershell
.\codex-route-guard.exe uninstall
```

## Safety properties

- Web routes must be HTTP loopback `/v1` endpoints.
- Web GPT launcher descriptors must match the expected launcher kind and loopback endpoint.
- Unknown providers, catalogs, and custom routes are not overwritten.
- Config writes are atomic.
- One Guard daemon runs per user session.
- Launch-race recovery is time-bounded and restricted to a fresh matching packaged-app family.
- Existing older ChatGPT/Codex sessions are not intentional recovery targets.
- Supported route transitions use the route owner's lifecycle instead of blind field edits.
- CI runs Linux tests, Windows tests, vet, Windows build, binary smoke checks, and publishes a checksum-bearing artifact.

## Documentation

- [Architecture](ARCHITECTURE.md) — ownership model, state machine, reconciliation loop, lifecycle and race handling.
- [Security](SECURITY.md) — trust boundaries and fail-closed behavior.
- [Compatibility](COMPATIBILITY.md) — supported integrations and explicit non-goals.
- [Contributing](CONTRIBUTING.md) — development and test workflow.

## Project direction

The long-term abstraction is a **Codex route ownership coordinator** with explicit adapters:

```text
Codex Route Guard
  |- route-owner adapters
  |- account/profile mutation adapters
  |- model-catalog ownership rules
  |- conflict reporting
  `- reconciliation policy
```

Adding an adapter should never weaken the core ownership rule: known ownership may be reconciled; unknown ownership must remain untouched.

## Binary trust

CI-produced Windows binaries are not Authenticode-signed, so Windows SmartScreen may show an Unknown publisher warning. Release packages include SHA-256 checksums.
