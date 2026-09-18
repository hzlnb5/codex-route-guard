# Compatibility

## Platform

- Windows 10/11: supported
- macOS: daemon installation not implemented
- Linux: daemon installation not implemented

## Integration matrix

| Component | Support | Notes |
| --- | --- | --- |
| Native Codex | Yes | Native fallback state |
| `codex-chatgpt-web` | Yes | First-class route-owner adapter |
| Cockpit Tools OAuth/profile switching | Yes | Supported mutation source |
| Known Cockpit model catalogs | Yes | Removed only when conflicting with active routed state |
| Microsoft Store Codex / ChatGPT family | Yes | Fresh-process recovery is packaged-app aware |
| Unknown custom provider | Preserved | Conflict instead of takeover |
| Different custom `openai_base_url` | Preserved | Not overwritten |
| Unknown `model_catalog_json` | Preserved | Not removed |
| Arbitrary third-party route journals | Not yet | Requires an explicit adapter |

## Expected scenarios

### Route owner live, account manager changes account

The account change is allowed. If the active owned route disappears, the Guard restores it. Known conflicting adapter-owned catalog state may be removed. Unknown third-party state is preserved.

### Route owner closed

After a grace period, the Guard asks the owner to disconnect through its own lifecycle and returns Codex to native routing.

### Route owner starts again

The Guard reconnects through the owner lifecycle. If Codex started milliseconds before reconnect completed, fresh-process recovery may reload only that just-started matching app family.

### Another router owns the config

If the current config clearly belongs to another provider or custom route, the Guard does not take ownership.

## Compatibility philosophy

Compatibility does not mean "the Guard always wins."

It means:

- known ownership is restored deterministically;
- unknown ownership is preserved;
- lifecycle-aware integrations remain internally consistent;
- unrelated existing sessions are not disrupted;
- conflicts are explicit rather than silently overwritten.
