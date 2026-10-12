# Himay (ᜑᜒᜋᜌ᜔)

*Himay* means to break down or analyze closely. It is a read-only terminal viewer that presents
OpenCode sessions as an execution and decision trace rather than a chat transcript.

```
go install github.com/0xGurg/himay/cmd/himay@latest   # or: go build -o himay ./cmd/himay
himay                         # browse projects and sessions
himay --session ses_…         # open one session directly
himay dump --session ses_… [--provenance]   # plain-text trace
himay doctor                  # schema/type diagnostics
himay --web                   # browser UI at http://127.0.0.1:7878 (opens automatically)
himay --web --session ses_…   # open one session in the browser
```

Web flags: `--addr 127.0.0.1:PORT` (loopback only; other hosts are refused), `--no-open`.

Flags: `--db PATH` (default `~/.local/share/opencode/opencode.db`, or `$HIMAY_DB`), `--ascii`
(or `HIMAY_ASCII=1`) if your terminal renders Baybayin poorly.

## Guarantees

- The database is opened with `mode=ro` and `query_only`; Himay has no write path and never
  copies, checkpoints, vacuums, or migrates it. WAL is honored (no `immutable=1`), so live
  sessions are shown consistently while OpenCode runs.
- Only data OpenCode stored is shown. Saved reasoning is displayed as-is; encrypted reasoning is
  labeled unreadable. Hidden chain-of-thought is never claimed or reconstructed.
- Evidence labels: `[recorded]` (stored by OpenCode), `[strong]` / `[possible]` (Himay's textual
  heuristics), `[unknown]`, `[derived]` (computed from recorded data, e.g. inferred agent switch).
  Correlations are never presented as recorded facts.

The web UI only answers GET requests, rejects any `Host` other than localhost (so other sites
cannot reach it via DNS rebinding), sends a strict Content-Security-Policy, and loads no external
assets: htmx, fonts, and CSS are embedded in the binary.

## Data sources

Reads OpenCode's current store only: `session_v2`, `session_message`, and `instruction_*`.
Legacy `session`/`message`/`part` tables are ignored in v1 (`himay doctor` reports any messages
that exist only there). Unknown message types and content items render as raw rows.

## Development

The web UI is Go templates + htmx, styled with Tailwind v4 via its standalone CLI (no Node/npm).
The compiled `internal/web/static/app.css` is committed, so `go build` works without Tailwind.
After editing templates or `internal/web/tailwind.css`, run `make css` (downloads the CLI into
`bin/` on first use) or `make css-watch`.

```
go test ./...                                   # fixture-based; never touches your DB
go test -run=^$ -fuzz=FuzzMessage ./internal/decode
HIMAY_REAL_DB=~/.local/share/opencode/opencode.db HIMAY_REAL_SESSIONS="ses_…" \
  go test -run TestRealDatabase ./internal/tui   # optional read-only render check
HIMAY_BROWSER_TEST=1 go test -run TestBrowserInteraction ./internal/web   # drives real Chrome
```

## License

MIT — see [LICENSE](LICENSE). Bundled third-party assets keep their own licenses: htmx (0BSD) and
the fonts in `internal/web/static/fonts/` (SIL Open Font License 1.1; license texts alongside).
