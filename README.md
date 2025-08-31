# Scale Cluster Agent

A lightweight agent that simulates many Kubernetes clusters on one machine using KWOK, then registers them with Rancher and maintains tunnels so Rancher can manage and proxy into them. It’s built for scale and functional testing without the cost of real clusters.

## How it works

- One KWOK “main cluster” runs locally.
- Each logical cluster you create is represented as a namespace in the main cluster (a “v-cluster”).
- The agent registers each logical cluster with Rancher (import flow) and keeps two tunnels healthy so Rancher can reach local APIs.
- A per-cluster `kubectl proxy` exposes the main KWOK API on a local port that Rancher reaches through the tunnels.
- Optional profiling (pprof) and apiserver audit policy can be enabled from the config file.

High-level flow per logical cluster:
1. Ensure the main KWOK cluster exists and serves HTTPS on MainAPIPort (default 8050).
2. Create/ensure a namespace on the main cluster for the logical cluster (e.g., `cluster-<name>`).
3. Request a Rancher import token and download its YAML (saved under `debug-yaml/` for troubleshooting).
4. Start/refresh tunnels and a local proxy so Rancher can manage and proxy into the KWOK API.
5. Proactively rotate credentials and reconnect to avoid token expiry issues.

## Prerequisites

- macOS or Linux with Go 1.21+.
- This repo’s `kwokctl` and `kwok` binaries must be executable for your platform (or replace them with matching platform binaries).
- `kubectl` on PATH.

Quick checks:

- `./kwokctl version`
- `./kwok --help`
- `kubectl version --client`

## Configure

Create `~/.scale-cluster-agent/config/config` (JSON, YAML, or simple `key: value` lines). Minimum:

- RancherURL: Rancher server URL
- BearerToken: Rancher API token (format `token-xxxx:secret`)

Other keys:

- ListenPort: HTTP API port for this agent (default 9090)
- LogLevel: info | debug
- MultiTenant: true to use a single KWOK main cluster and namespaces for v-clusters
- MainClusterName: default `main-cluster`
- MainAPIPort: HTTPS port for KWOK apiserver (default 8050)
- ProxyBasePort: starting port for per-cluster kubectl proxies (default 8440)
- Pprof: true to enable pprof on PprofPort (default 6060)
- PprofPort: pprof port (default 6060)
- MemLogIntervalSec: if > 0, logs Go memory stats every N seconds
- Audit: true to apply an apiserver audit policy to the KWOK apiserver

Example (YAML):

```
RancherURL: https://your-rancher.example
BearerToken: token-xxxxx:yyyyyyyyyyyyyyyy
ListenPort: 9090
LogLevel: debug
MultiTenant: true
MainClusterName: main-cluster
MainAPIPort: 8050
ProxyBasePort: 8440
Pprof: true
PprofPort: 7070
MemLogIntervalSec: 30
Audit: true
```

Optional files:

- `~/.scale-cluster-agent/config/audit-policy.yaml` — applied when `Audit: true`.
- `~/.scale-cluster-agent/config/cluster.yaml` — template used to populate new logical clusters.

Notes:

- Pprof is controlled by the config file key `Pprof`. If the config file exists, environment variables won’t override it.
- Audit requires both `Audit: true` and `audit-policy.yaml`. The agent passes the policy using `kwokctl --kube-audit-policy <path>`.

## Run

Build and run:

```
go build -o scale-cluster-agent ./
./scale-cluster-agent
```

On start, the agent:

- Serves HTTP on `:ListenPort` (default 9090)
- Starts a small ping server on `127.0.0.1:6080/ping`
- Ensures the main KWOK cluster exists, is serving HTTPS on `MainAPIPort`, and (optionally) applies the audit policy
- Rehydrates any previously created logical clusters and attempts to reconnect to Rancher

## HTTP API

- `GET /health` — basic liveness
- `GET /clusters` — list clusters
- `POST /clusters` — create a logical cluster (JSON body: `{ "name": "my-cluster" }`)
- `DELETE /clusters/{name}` — delete a logical cluster

Creating a cluster triggers:

- Namespace creation in the main KWOK cluster
- Rancher registration token acquisition and YAML download (debug YAML lands in `debug-yaml/`)
- Starting/refreshing a local `kubectl proxy` on a dedicated port
- Tunnels to Rancher so Rancher can manage and proxy into the KWOK API

## Rancher connectivity and proxying

- The agent establishes and maintains the required tunnels to Rancher (register and cluster-agent flows).
- Credentials are refreshed proactively to avoid token expiry disconnects.
- Rancher can proxy into the local KWOK apiserver endpoints through these tunnels; the agent runs a per-cluster `kubectl proxy` bound to `127.0.0.1:<port>` for Rancher to reach via the tunnel.

## Observability & debugging

- pprof: if enabled (`Pprof: true`), pprof endpoints are on `http://localhost:<PprofPort>/debug/pprof/`.
- Memory logs: if `MemLogIntervalSec > 0`, memory statistics are logged periodically.
- KWOK data lives under `~/.kwok/clusters/<cluster-name>/` (the main cluster’s kubeconfig is used for kubectl).
- Agent state is saved atomically to `~/.scale-cluster-agent/state.json` (with `.bak` fallback on corruption).

## Troubleshooting

- Main cluster stuck or misconfigured: remove `~/.kwok/clusters/main-cluster` and restart the agent to recreate cleanly.
- Can’t reach KWOK API (`127.0.0.1:8050` refused): ensure main cluster is serving HTTPS; the agent auto-detects HTTP/HTTPS mismatch and will recreate if needed.
- Rancher “Unauthorized” after ~1 hour: the agent rotates credentials on a timer and rebuilds tunnel parameters for reconnects; check logs for rotation activity.
- Debug YAML noise in Git: `.gitignore` excludes `debug-yaml/`. To stop tracking already committed files: `git rm -r --cached debug-yaml && git commit -m "chore: stop tracking debug-yaml"`.

## Development notes

- Start order: the agent’s HTTP server starts immediately so the API is responsive even while KWOK is created.
- KWOK create uses `kwokctl` with `--kube-apiserver-port` and (optionally) `--kube-audit-policy` when `Audit: true`.
- Proxies: `kubectl proxy` processes are launched per logical cluster; ports auto-increment from `ProxyBasePort` and are persisted.
- Build with `go build ./...`. No external build system required.

## Caveats

- This is a simulation layer (KWOK), not a full Kubernetes implementation.
- Some Rancher features that depend on real cluster behavior may not function identically with KWOK.
