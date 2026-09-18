# codex-upstream-switcher

A small [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) dynamic
plugin that changes the Codex upstream URL for existing Codex OAuth
credentials.

It only implements the `AuthProvider.ParseAuth` hook. For a Codex OAuth JSON
file it:

1. preserves the original credential JSON and token metadata;
2. preserves the native Codex `plan_type` model-routing attribute;
3. adds `Attributes["base_url"]` with the configured upstream URL.

CPA continues to own the Codex executor, OAuth login, refresh-token exchange,
account selection, cooling, retry, and WebSocket behavior.

## Security warning

This plugin causes CPA to send the selected Codex access token and requests to
the configured upstream. 

The plugin does not log or persist tokens itself. The upstream still receives
whatever CPA's native Codex executor sends to it.

## Installation

The release assets are compatible with the CPA plugin store layout. Install the
matching archive for the host platform, or place the dynamic library directly
under CPA's configured plugin directory:

| Platform | Library |
| --- | --- |
| Linux | `codex-upstream-switcher.so` |
| Windows | `codex-upstream-switcher.dll` |
| macOS | `codex-upstream-switcher.dylib` |

The archive contains exactly one library at its root.

## Configuration

Enable the plugin and set the upstream in `config.yaml`:

```yaml
plugins:
  enabled: true
  configs:
    codex-upstream-switcher:
      enabled: true
      base-url: "https://codex-relay.oaifree.com/backend-api/codex"
```

`base-url` may be changed to another absolute `http` or `https` URL. Trailing
slashes are removed. Query strings, fragments, and URL userinfo are rejected.

To disable routing, disable the plugin and restart CPA (or remove the plugin
configuration):

```yaml
plugins:
  configs:
    codex-upstream-switcher:
      enabled: false
```

When disabled, CPA's native Codex parser and default upstream are used.

### Configuration reload behavior

The plugin accepts CPA's `plugin.reconfigure` call and updates its configured
URL for future parses. CPA's current plugin ABI does not expose a runtime
"reparse all existing auth files" operation. Therefore, after changing
`base-url`, restart CPA (recommended) or touch/re-save each Codex credential
file so the watcher parses it again. Existing runtime Auth records keep their
previous `base_url` until they are re-parsed.

## Compatibility boundary

The plugin claims the `codex` auth-provider identifier so CPA can invoke its
file parser. It intentionally does not implement:

- `auth.login.start`
- `auth.login.poll`
- `auth.refresh`

CPA's explicit `/v0/management/codex-auth-url` route and native
`CodexAutoExecutor.Refresh` remain in charge of those operations on current CPA
releases. Do not use this plugin with a fork that routes Codex OAuth refresh
through plugin AuthProvider callbacks unless that fork adds an explicit
"not handled" result to the ABI.

## Build from source

Go 1.26 and a C compiler are required for dynamic-library builds.

```bash
go test ./...
make VERSION=0.1.0 build
```

For a release build, use the workflow in `.github/workflows/release.yml`.

## License

MIT. See [LICENSE](LICENSE).
