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

The online installer downloads the latest release and checks its SHA-256. Both
installers print the public HTTPS panel address when its socket is active, or
the default loopback address otherwise. A fresh install prints the initial
administrator credentials. Upgrades keep the existing password.

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

To forward an additional public TCP port to a single private backend while the
engine stays online, use the [TCP relay setup](README.fa.md#هدایت-یک-پورت-عمومی-tcp-به-مقصد-خصوصی).
To show that traffic in the dashboard, use the
[migration to a measured engine listener](README.fa.md#انتقال-ورودی-tcp-به-موتور-برای-نمایش-آمار).

## Linux data plane and Go control plane

On a dedicated Linux IPv4 router, set up `/etc/backpack-router/kernel-forward.json`
for the public interface, private interface and backend pool before installation.
The installer wires the kernel forwarder and dashboard metrics automatically.
With eligible backends, nftables forwards all new connections by default. Go
manages configuration and health checks and can provide a fallback listener.
The dedicated installation steps are in the [Persian setup guide](README.fa.md#موتور-اصلی-لینوکس-و-مسیر-پشتیبان-go).

For a fresh install, provide the interface and backend addresses directly:

```sh
curl -fsSL https://raw.githubusercontent.com/dr-hoseyn/backpack-smart-router/main/install-online.sh | bash -s -- \
  --kernel --public-interface eth0 --public-ip 203.0.113.10 \
  --private-interface eth1 --private-ip 10.0.0.2 --port 8443 \
  --backend 10.0.0.11 --backend 10.0.0.12
```

Replace the example addresses and interfaces. Use `--backend-port` if the
backends listen on a different port. The setup uses Python 3, iproute2, nftables
and curl. Install `conntrack` to include kernel connection counts in the dashboard.
For TLS forwarding, add `--tls-server-name tunnel.example.net` to require a
verified TLS handshake before nftables sends new connections to a backend.
For an ingress that needs SYN admission limits, set both `--syn-rate` and
`--syn-rate-per-ip` in connections per second. Keep the per-IP rate at or below
the total. The generated rules reserve conntrack mark bit `0x10000000`.

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
