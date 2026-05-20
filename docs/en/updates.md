# AmneziaWG Updates

The awg-forge Docker image contains pinned upstream AmneziaWG tools:

- `amneziawg-go`;
- `amneziawg-tools`.

Pinned refs live here:

```bash
cat build/amneziawg.refs
```

## Update Decision

awg-forge does not update AmneziaWG tools inside a running container automatically.

The update workflow is manual:

1. Check whether upstream has newer commits.
2. Update pinned refs in the awg-forge repository.
3. Rebuild the Docker image.
4. Test real tunnels and clients.
5. Release a new awg-forge image.

This keeps builds reproducible and reduces the risk of unexpectedly breaking working VPN tunnels.

## Check Updates Locally

```bash
make updates-local
```

## Check Updates In Docker

```bash
make updates-docker
# or
docker exec awg-forge awg-forge updates
```

## Update pinned refs for a future PR

```bash
make update-amneziawg-refs
```

Then build and test:

```bash
make docker-build
```

## Web UI

The `Updates` button performs the same read-only upstream ref check.

It does not modify the running system, download new binaries, or restart tunnels.
