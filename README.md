# Unraid Template Sync

A small, standard-library-only Go service that receives authenticated GitHub
push webhooks and refreshes private Unraid Community Applications templates.
On its first start it clones the configured repository into an empty mounted
directory; later starts and webhooks fetch and reset that managed checkout.

```text
GitHub webhook
  -> Nginx Proxy Manager (TLS and GitHub hooks IP allowlist)
  -> Unraid Template Sync (HMAC, repository and branch validation)
  -> git fetch/reset
  -> private/Lowess/*.xml
```

## Security model

Nginx Proxy Manager is responsible for allowing only the current networks in
the `hooks` field returned by `https://api.github.com/meta`. GitHub changes
these ranges periodically, so the NPM Access List must be kept current.

The service independently requires GitHub's `X-Hub-Signature-256` HMAC,
accepts only the configured repository and branch, suppresses duplicate
delivery IDs, limits request bodies and HTTP timeouts, and never mounts the
Docker socket. IP allowlisting is intentionally not implemented in the app.

## Container

Images are published by GitHub Actions to:

```text
ghcr.io/lowess/unraid-template-sync:latest
```

Every pull request is tested and built. Pushes to `main` publish `latest` and
commit-SHA tags. Tags such as `v1.2.3` also publish semantic-version tags.

The GHCR package must be public for an unauthenticated Unraid installation to
pull it. After its first publication, set the package visibility to public in
GitHub's package settings.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `WEBHOOK_SECRET` | required | High-entropy secret shared with GitHub |
| `GITHUB_REPOSITORY` | `Lowess/docker-templates-unraid` | Accepted webhook repository |
| `GITHUB_BRANCH` | `main` | Accepted push branch |
| `GIT_REMOTE_URL` | public HTTPS repository URL | Source used by `git fetch` |
| `REPO_DIR` | `/repo` | Managed checkout, cloned when empty |
| `SOURCE_SUBDIR` | `Lowess` | XML source beneath the checkout |
| `DEST_DIR` | `/templates` | Private CA template destination |
| `PORT` | `9000` | HTTP listener port |
| `SYNC_ON_START` | `true` | Reconcile after container startup |
| `GIT_CLEAN` | `true` | Remove untracked checkout files |

The service deliberately clones and fetches the public repository over HTTPS
instead of mounting host SSH credentials. No PAT is needed. If `/repo` has no
`.git` metadata, it must be empty; the service refuses to overwrite unrelated
files. `GIT_CLEAN=true` matches the previous script and means local untracked
files in the managed checkout are deleted after it has been cloned.

## Endpoints

- `POST /webhook` validates and queues eligible GitHub webhook deliveries.
- `GET /healthz` reports the latest synchronization result.

## Nginx Proxy Manager and GitHub

1. Create an NPM Access List containing every current GitHub `hooks` CIDR and
   deny all other sources. Do not add browser authentication.
2. Proxy an HTTPS hostname to container port `9000`, or the Unraid mapped port.
3. Generate a secret with `openssl rand -hex 32`.
4. Put that secret in the Unraid template and the GitHub repository webhook.
5. Set the GitHub payload URL to `https://YOUR-HOST/webhook`, content type to
   `application/json`, and subscribe only to push events.

The current hook CIDRs can be inspected with:

```bash
curl -fsSL https://api.github.com/meta | jq -r '.hooks[]'
```

## Development

```bash
go test -race ./...
go vet ./...
docker build -t unraid-template-sync:dev .
```
