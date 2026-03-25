# Local NeoFS dev environment

For repeatable local testing, use the NeoFS development environment provided by NSPCC.

## Option A: use `nspcc-dev/neofs-dev-env`

- Clone `nspcc-dev/neofs-dev-env`
- Follow its README to start the local NeoFS stack (typically via Docker Compose)
- Use the provided wallet key file(s) (often under `wallets/`)
- Use the exposed gRPC endpoint (often something like `s03.neofs.devenv:8080` inside the dev network)

## Running `neofs-mount` against dev env

```bash
make build
mkdir -p /tmp/neofs

./bin/neofs-mount \
  --endpoint <host:port> \
  --wallet-key /path/to/wallet.key \
  --mountpoint /tmp/neofs \
  --cache-dir /tmp/neofs-cache \
  --cache-size $((1024*1024*1024))
```

Unmount:

```bash
fusermount3 -u /tmp/neofs
```

## Integration tests

The integration test suite is opt-in and requires FUSE + a running NeoFS endpoint.

```bash
NEOFS_ENDPOINT=<host:port> \
NEOFS_WALLET_KEY=/path/to/wallet.key \
NEOFS_TEST_CONTAINER_ID=<base58 container id> \
go test -tags=integration ./...
```

