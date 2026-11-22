# BackPack Smart Router

BackPack Smart Router balances new TCP connections and UDP flows across private
entrances to existing tunnels. It includes a Persian RTL dashboard and runs its
management and forwarding services independently.

- [Persian installation and operations guide](README.fa.md)
- [Ubuntu packages and SHA-256 checksums](https://github.com/dr-hoseyn/backpack-smart-router/releases)

## One-line Ubuntu installation

```sh
curl -fsSL https://raw.githubusercontent.com/dr-hoseyn/backpack-smart-router/main/install-online.sh | bash
```

The installer checks the package SHA-256 and prints the default panel URL
and, on a fresh install, the initial administrator credentials. The panel
listens on the server's loopback interface by default.

```text
v2rayNG → public router listener → selected private VM entrance → existing tunnel → egress
```

Existing flows keep their route. A reconnect can use a different backend and
public egress IP. Authentication and TLS run at the endpoints already configured
in the existing tunnel path. Before moving production traffic, test one actual
client profile through all six backends, including its hostname and security
settings.

## Build and test

```sh
go test ./...
go test -race ./...
go test -run '^$' -bench . -benchmem ./internal/router
BACKPACK_TEST_CONNECTIONS=5000 go test -count=1 -run TestConcurrentForwarding -v ./internal/router
npm --prefix ui ci
npm --prefix ui run build
bash deploy/package.sh v0.1.0 amd64
```

Go 1.27.4 and Node.js 24 are used in CI. The release includes the engine, panel,
VM health agent, systemd units, and sample settings. The application preserves
the byte stream or datagram contents and uses the router's own upstream sockets.
Backends see the router as the network-layer source.

For deployments with a separate public ingress, set `VITE_PUBLIC_INGRESS` when
building the UI to display its public address and port on the topology map.

## Management API

Use the private dashboard address. Log in with `POST /api/v1/login` and retain
the session cookie. Mutations use the `X-CSRF-Token` from the login or session
response. API requests with a body use JSON.

- `GET /api/v1/status` — current counters, health and the latest routing decision
- `GET /api/v1/live` — status stream over SSE
- `GET /api/v1/config`, `PUT /api/v1/config` — versioned whole-configuration read and update
- `POST /api/v1/backends/{id}/check` — run the backend's health check
- `GET /api/v1/metrics?range=5m` — time buckets, with ranges `15m`, `1h`, `24h` and `7d`
- `GET /api/v1/events?hours=24` — latest events, optionally filtered by `kind` and `backend`
- `GET /api/v1/admins`, `POST /api/v1/admins`, `DELETE /api/v1/admins/{name}` — administrator management
- `PUT /api/v1/password`, `POST /api/v1/logout` — account password and session control

A configuration update with HTTP 202 is durable in SQLite and pending application
by the engine. Compare the status revision with the saved configuration revision.
Listener and group changes use the maintenance command in the operations guide.
