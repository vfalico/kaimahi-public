# Current public kagent client descriptor provenance

Source: https://github.com/kagent-dev/kagent, canonical Go import
`github.com/kagent-dev/kagent/go`; current base
`a0ad6cb98b02bf4a3537a2b6fdc04e71f315bdf3` plus the separately delivered governed
Session API companion changes (through `b4ae4dd89be9bd97214d5aef1323eb604d9a1ece`).
Source proto license: Apache-2.0 (the kagent repository LICENSE).
The embedded descriptor carries public schemas and their transitive imports,
not generated clients or product internals. Unknown request fields fail closed.

From this directory, with reconstructed kagent checkout in `$KAGENT`:

```sh
buf --version # qualified: 1.47.2
buf build "$KAGENT/proto" \
 --type kagent.api.v1.AgentService --type kagent.api.v1.ModelService \
 --type kagent.api.v1.HarnessService --type kagent.api.v1.SessionService \
 --as-file-descriptor-set --exclude-source-info -o public.fds
sha256sum public.fds
```

Expected SHA256:
`469f16c6b1e5e1d79b08674d86d0d76c48a72f6e88c22681d46e456f3af2a8e8`.
Canonical `proto/buf.lock` pins dependencies. No floating schema download,
reflection service or runtime-branded enforcement protocol is required.
The normal client consumes only public gRPC and A2A HTTP endpoints and opaque
requirement bytes. Runtime owns execution and enforcement lifecycle.
