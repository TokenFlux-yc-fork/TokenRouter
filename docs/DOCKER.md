# TokenRouter Docker Image

TokenRouter publishes multi-architecture images to GitHub Container Registry:

```text
ghcr.io/tokenflux/tokenrouter:latest
```

The application requires PostgreSQL and Redis. Docker Compose is the supported
way to supply the complete runtime configuration, persistent storage, health
checks, and dependency ordering.

## Docker Compose

Choose the configuration that matches the deployment topology:

| File | Services | Storage | Intended use |
| --- | --- | --- | --- |
| [`deploy/docker-compose.local.yml`](../deploy/docker-compose.local.yml) | TokenRouter, PostgreSQL, Redis | Local directories | Production deployments that prioritize simple backup and migration |
| [`deploy/docker-compose.yml`](../deploy/docker-compose.yml) | TokenRouter, PostgreSQL, Redis | Docker named volumes | Production deployments managed through Docker volumes |
| [`deploy/docker-compose.standalone.yml`](../deploy/docker-compose.standalone.yml) | TokenRouter only | Docker named volume | Existing external PostgreSQL and Redis services |
| [`deploy/docker-compose.dev.yml`](../deploy/docker-compose.dev.yml) | Locally built TokenRouter, PostgreSQL, Redis | Local directories | Development and source testing |

For a complete deployment, follow [DEPLOY_GUIDE.md](./DEPLOY_GUIDE.md). The
example environment file is [`deploy/.env.example`](../deploy/.env.example).

## Standalone Container

Direct `docker run` deployments must provide the same settings as the
`sub2api` service in `docker-compose.standalone.yml`. At minimum, configure:

| Variable | Purpose |
| --- | --- |
| `AUTO_SETUP=true` | Enables unattended container initialization |
| `SERVER_HOST=0.0.0.0` | Makes the application listen inside the container |
| `DATABASE_HOST`, `DATABASE_PORT` | PostgreSQL endpoint |
| `DATABASE_USER`, `DATABASE_PASSWORD`, `DATABASE_DBNAME` | PostgreSQL credentials and database |
| `DATABASE_SSLMODE` | PostgreSQL TLS mode |
| `REDIS_HOST`, `REDIS_PORT` | Redis endpoint |
| `REDIS_USERNAME`, `REDIS_PASSWORD`, `REDIS_DB` | Redis credentials and database |
| `JWT_SECRET` | Stable signing key for login sessions |
| `TOTP_ENCRYPTION_KEY` | Stable encryption key for two-factor authentication |

Persist `/app/data`, bind the published port to the intended host interface,
and apply the security and resource limits from the standalone Compose file.
The application does not use the legacy `DATABASE_URL` or `REDIS_URL`
variables.

## Supported Architectures

- `linux/amd64`
- `linux/arm64`

## Tags

- `latest`: latest stable release
- `vX.Y.Z`: immutable release version

Production deployments should pin a release tag or image digest and validate
database backups before upgrading.

## Links

- [GitHub repository](https://github.com/TokenFlux/TokenRouter)
- [Deployment guide](./DEPLOY_GUIDE.md)
