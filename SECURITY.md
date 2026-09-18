# Security and Trust Model

## Scope

Codex Route Guard is a local Windows coordination utility. It is not an authentication service and it is not a network proxy.

## Trust boundaries

The Guard trusts:

1. the current Windows user account;
2. the configured `codex-chatgpt-web` integration journal/runtime command under that user;
3. the Codex config path recorded by that integration;
4. loopback endpoints that satisfy route validation.

It does not treat arbitrary current config values as owned state.

## Route validation

A Web route must be:

- HTTP;
- loopback (`127.0.0.1`, `localhost`, or `::1`);
- explicitly bound to a port;
- using the `/v1` path.

## Unknown ownership

The Guard refuses to silently replace:

- unknown custom `model_provider`;
- unknown model catalog;
- a different custom API base URL.

## Process recovery

Launch-race recovery is deliberately narrow. It does not issue a broad `Get-Process ChatGPT,Codex | Stop-Process`.

Recovery only considers fresh processes whose start time correlates with the config mutation and whose packaged app family can be resolved unambiguously. Older sessions are not intentional recovery targets.

## Privileges

Installation is per-user and does not require administrator rights.

State and logs are stored under:

```text
%LOCALAPPDATA%\CodexRouteGuard\
```

Autostart uses the current user's Windows Run key.

## Network behavior

The Guard's direct health checks are loopback-only. Route lifecycle operations are delegated to the configured route-provider runtime command.

## Binary trust

CI binaries are not Authenticode-signed. Windows SmartScreen may show Unknown publisher. Release artifacts include SHA-256 checksums.
