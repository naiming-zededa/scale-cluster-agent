# Cluster Creation & Connection Flow (Multi-Tenant KWOK Mode)

This document describes the end‑to‑end sequence from a user POSTing a new cluster to the agent, through KWOK virtual cluster setup, Rancher import, remotedialer session establishment, and steady state operations.

---
## 1. User Initiation
1. User request:
   ```bash
   curl -sS -X POST http://localhost:9090/clusters \
     -H 'Content-Type: application/json' \
     -d '{"name":"test-cluster-012"}'
   ```
2. HTTP handler (`createClusterHandler`):
   - Validates name & uniqueness.
   - Allocates a `ClusterInfo` (Status = creating/pending).
   - Persists agent state (atomic write to `~/.scale-cluster-agent/state.json`).
   - Spawns `runCompleteClusterSetup(ci)` goroutine.

## 2. Cluster Setup (Multi-Tenant Path)
3. Ensure *main* KWOK cluster exists (`EnsureMainCluster`). If absent, create HTTPS apiserver at configured `MainAPIPort`.
4. Create tenant namespace: `cluster-<name>` (idempotent).
5. Start / reuse an in‑process lightweight API reverse proxy (Go `httputil.ReverseProxy`) bound to a reserved local port (e.g. `127.0.0.1:8440+N`) targeting the **main** KWOK apiserver.
   - No external `kubectl proxy` process is spawned (lower overhead, simpler lifecycle).
   - Port selection + reservation prevents races; server started directly in agent goroutine.
   - Direct pass‑through of all API paths (no path rewriting, no auth mutation—main KWOK assumed locally accessible).
   - Compression disabled (removes `Accept-Encoding`) to simplify Rancher/debug inspection.
   - Future hardening: optional namespace scoping / verb filtering.
6. Populate tenant resources (`populateTenantFromTemplate`):
   - Apply Node manifests (metadata + labels: `cluster-id`, `logical-cluster`, role labels, kwok marker).
   - Patch each Node's **status.capacity** and **status.allocatable** (post-create) so Rancher will compute real totals (new logic added).
   - Apply template Pods in tenant namespace, pinning `nodeName` to created nodes.
7. Mark cluster `Status=ready` after minimal resources and proxy are in place; persist state.

## 3. Rancher Import / Credential Bootstrap
8. Fetch Rancher import YAML (`GET /v3/import/<registrationToken>.yaml`).
9. Transform (namespace scoping for multi-tenant) & apply to main cluster (tenant namespace) -> creates:
   - ServiceAccount(s) (cattle-agent / cattle-cluster-agent)
   - Secret containing auth token (`cattle-credentials-*` possibly suffixed per tenant)
10. Poll for Secret; extract bearer token.
11. Extract CA cert from KWOK kubeconfig if needed for TLS trust.

## 4. Remotedialer Sessions
Two websocket tunnels are opened:

A. Cluster Agent Session
- URL: Rancher cluster-agent connect endpoint.
- Header: `Authorization: Bearer <cluster-agent-token>`.
- On success, a `Session` is created and pings start.

B. Steve / Proxy (stv-cluster) Session
- Separate websocket with header: `Authorization: Bearer stv-cluster-<clusterName>`.
- Provides an `allowFunc` restricting dial targets (local `kubectl proxy` port + health endpoints).

## 5. Remotedialer Message Exchange
| Phase | Direction | Message / Frame | Purpose |
|-------|-----------|-----------------|---------|
| Keepalive | Client→Server | WebSocket Ping | Liveness (interval = `PingWriteInterval`) |
| Connection request | Server→Client | `connect` control msg | Ask client to open local TCP (e.g. to API) |
| Open local | Client | Local dial → ack frame | Establish tunnel (assign connID) |
| Data | Bi-directional | Binary frames tagged by connID | Stream payload (HTTP, exec, logs, etc.) |
| Sync | Client→Server | syncConnections | Reconcile active conn IDs |
| Close | Either | close frame | Terminate specific connID |

Error handling: timeouts or dial failures close the connID; websocket drop triggers full reconnect.

## 6. Resource Visibility in Rancher
12. Rancher (through tunnel) LISTs / WATCHes Nodes / Pods via the agent’s in‑process reverse proxy.
13. Node status (capacity/allocatable) now present → clusterstats aggregator sums realistic totals.
14. Pods affect requested/limits (if resource requests defined in template).
15. Rancher transitions cluster to Active when health criteria met.

## 7. Steady State Loops (Agent Side)
- Ping ticker (keep websocket alive).
- Sync connections ticker (housekeeping).
- Periodic cluster data / status logs (optional).
- Deletion watcher (detect server-side cluster removals -> cleanup local state, KWOK artifacts, proxy).
- Optional: memory / pprof logging when configured.

## 8. Reconnection & Resilience
| Event | Detection | Action |
|-------|-----------|--------|
| Websocket error / EOF | Read loop fails | Mark disconnected; schedule reconnect with throttle |
| In‑process proxy server error | ListenAndServe error / health probe (future) | Restart proxy on new port; update mapping |
| Rancher deletes cluster | Deletion watcher sees missing ID | Stop proxy, remove tenant resources/state |
| Node status drift (future) | Reconcile loop (planned) | Re-patch status |

## 9. Restart Path
16. Agent restart → `LoadState()` restores clusters & proxy port map.
17. `RehydrateFromDisk()` enumerates KWOK clusters; starts any stopped one.
18. For each ready cluster: start websocket reconnect; best-effort re-populate missing template objects.
19. Node status persists (already patched), so Rancher quickly recomputes resource totals.

## 10. Key Log Lines (Chronological Sample)
```
populateTenantFromTemplate: start for test-cluster-012 (tenant-ns=cluster-test-cluster-012)
Applying node test-cluster-012-node1: capacity(cpu=..., memory=..., pods=...) allocatable(...)
Patched node/test-cluster-012-node1 status (capacity/allocatable)
Starting in-process API proxy for cluster test-cluster-012 on 127.0.0.1:8447 -> http://127.0.0.1:<MainAPIPort>
Cluster test-cluster-012 is ready, connecting to Rancher
... (steady pings suppressed at info level) ...
```

## 11. Simplified Sequence Diagram
```
User  -> Agent API : POST /clusters {name}
Agent -> KWOK(main): ensure main cluster
Agent -> KWOK(main): create namespace cluster-<name>
Agent -> KWOK(main): apply Nodes & Pods
Agent -> KWOK(main): patch Node status
Agent -> local: start in-process API reverse proxy (port P)
Agent -> Rancher : GET import YAML
Agent -> KWOK(main): apply import YAML (SA + Secret)
Agent -> KWOK(main): read Secret token
Agent -> Rancher : WS connect (cluster-agent)
Agent -> Rancher : WS connect (stv-cluster)
Rancher -> Agent : connect (tcp 127.0.0.1:P)
Agent -> KWOK(main): dial & proxy data
(loop) : pings, sync, watches, data streams
```

## 12. Remotedialer Frame Summary
| Type | Trigger | Notes |
|------|---------|-------|
| Ping (WS control) | Interval | Keeps session alive |
| Connect | Rancher needs new logical TCP | Includes connID, proto, address |
| Data | Any tunneled stream | Multiplexed by connID |
| Close | Stream end/error | Removes conn mapping |
| SyncConnections | Periodic client tick | Prunes stale connIDs |

## 13. Extension Ideas
- Add pod resource requests to show requested/limits metrics.
- Periodic reconciliation of Node.status to guard against accidental modification.
- Jittered exponential backoff for reconnect attempts.
- Health probe & self-heal loop for in-process proxy (graceful restart, metrics).

## 14. Glossary
| Term | Meaning |
|------|---------|
| KWOK | Kubernetes WithOut Kubelet; simulator for control-plane objects |
| Tenant Namespace | Per logical cluster namespace `cluster-<name>` in main KWOK |
| In-process API proxy | Lightweight Go reverse proxy (mux + httputil) exposing main KWOK API per logical cluster port |
| Remotedialer | Rancher websocket-based TCP tunnel multiplexer |
| stv-cluster session | Steve-style header-only auth tunnel for API access |

---
**End of document**
