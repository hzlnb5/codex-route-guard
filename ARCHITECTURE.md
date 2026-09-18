# Architecture

## Design goal

Codex Route Guard is a local reconciliation layer for a shared Codex configuration surface. It is not a model router. Its job is to keep independently managed integrations from accidentally invalidating one another.

Core rule:

> Reconcile known ownership; refuse unknown ownership.

## Shared control plane

`~/.codex/config.toml` can carry several independent concerns:

- authentication/profile state;
- Responses API routing;
- model-provider selection;
- model catalog selection;
- hooks and integration metadata;
- native fallback state.

Different applications can own different concerns without sharing a transaction.

```text
Account/Profile manager ----\
Route provider -------------+--> config.toml --> Codex
Catalog manager ------------/
              ^
              |
       Codex Route Guard
     reconciles known ownership
```

## Ownership source

For `codex-chatgpt-web`, ownership is read from the route owner's own integration journal. The journal identifies the config path, active state, installed route, and restoration state.

This is stronger than guessing ownership from the current file, because the current file may already have been modified by another process.

## Reconciliation loop

1. Load Guard mode.
2. Refresh route-owner journal.
3. Detect owner runtime presence/health.
4. Decide desired state: routed or native.
5. Use owner lifecycle for connect/disconnect.
6. Watch config mutations while routed state is active.
7. Repair only fields with proven ownership.
8. Fail closed on unknown ownership.

## Lifecycle-aware switching

Blindly restoring `openai_base_url` is insufficient when a route provider maintains a journal, hooks, realtime endpoint assignment, caches, or previous-state snapshots.

Supported transitions therefore invoke the provider's own lifecycle where possible:

```text
route connect
route disconnect
```

Direct config repair is reserved for a narrow case: an external tool changed a field already owned by the active route provider.

## Conflict rules

The Guard treats the following as conflicts rather than overwrite targets:

- unknown non-OpenAI `model_provider`;
- unknown `model_catalog_json`;
- a custom `openai_base_url` that differs from the owned route.

Known Cockpit-managed model catalogs are an explicit adapter rule, not a generic wildcard.

## Launch-race handling

A subtle failure can happen even if the final file is correct:

```text
T0 external tool rewrites config
T1 Codex starts and reads stale/native state
T2 Guard restores routed state
T3 file is correct, process still holds stale startup state
```

Windows recovery therefore correlates process start time with the triggering config write. It considers only very recent ChatGPT/Codex packaged processes, resolves exactly one matching app family, and reloads only that fresh family.

If identity cannot be proven safely, recovery does nothing.

## Atomicity

Config changes are written through a temporary file and replaced atomically.

## Single instance

A named Windows mutex prevents two Guard daemons from racing with each other.

## Failure model

The Guard prefers safe non-action over speculative takeover:

- journal missing -> do not invent a route;
- unknown provider -> preserve and report conflict;
- unknown catalog -> preserve;
- non-loopback route -> reject;
- invalid launcher descriptor -> reject;
- ambiguous fresh process identity -> do nothing.
