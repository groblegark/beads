# Epic: 100% HTTP Beads — Local↔Cloud Agent Interop

**Epic ID**: bd-930h6 (HTTP) + bd-89oz2 (Interop)
**Priority**: P1
**Status**: Open
**Date**: 2026-02-23

## Vision

Make beads work **100% over HTTP** with zero direct NATS or Unix socket dependencies on the client side. This enables local CLI agents to seamlessly interoperate with cloud-hosted daemon instances — and vice versa.

A developer's local `bd` CLI should be able to create/claim/close issues on a cloud daemon, subscribe to live events from cloud agents, and participate in the same event bus as Kubernetes-hosted agents. Cloud agents should be able to interact with any daemon reachable over HTTP.

## Current State

The beads daemon is **~95% HTTP-capable** today:

| Layer | HTTP Status | Notes |
|-------|-------------|-------|
| RPC operations | **159/165 mapped** | 6 missing (session gates, dep tree, mol progress) |
| Event streaming | **SSE endpoints exist** | `/events` (mutations), `/bus/events` (all 10 streams) |
| Authentication | **Bearer + per-rig keys** | `BD_DAEMON_TOKEN`, `BD_RPC_AUTH_KEYS` |
| CORS | **Supported** | `BD_CORS_ORIGINS` env var |
| Health/readiness | **Full K8s probes** | `/health`, `/livez`, `/readyz`, `/metrics` |
| CLI transport | **Auto-detects** | `BD_DAEMON_HTTP_URL` → HTTP, else Unix socket |

### What's Missing (the 5%)

1. **Session gates** (4 operations) — hardwired to NATS KV store, no HTTP path
2. **Local watch prefers direct NATS** — `bd watch` bypasses HTTP for local daemons
3. **6 missing HTTP method mappings** — session gates, dep tree, mol progress
4. **No SSE reconnection** — client doesn't use `Last-Event-ID` for resume
5. **No cross-daemon federation auth** — can't trust events across daemon boundaries

## Architecture

### Current Transport Stack

```
┌─────────────┐         ┌─────────────────────┐
│  bd CLI     │         │  bd daemon           │
│             │         │                      │
│  Unix sock ─┼────────►│  RPC server          │
│  HTTP RPC  ─┼────────►│  HTTP server (:9080) │
│  NATS direct┼────────►│  NATS embedded       │
│  SSE client ┼────────►│  SSE endpoints       │
└─────────────┘         └─────────────────────┘
```

### Target: HTTP-Only Stack

```
┌─────────────┐   HTTPS   ┌─────────────────────┐
│  bd CLI     │           │  bd daemon           │
│  (local or  │           │  (local or cloud)    │
│   cloud)    │           │                      │
│             │           │                      │
│  HTTP RPC  ─┼──────────►│  /bd.v1.BeadsService/│
│  SSE stream ┼──────────►│  /events             │
│  SSE bus   ─┼──────────►│  /bus/events         │
│  Health    ─┼──────────►│  /health, /readyz    │
└─────────────┘           └─────────────────────┘
         ▲                         │
         │    SSE keepalive        │
         └─────────────────────────┘
```

## Epic 1: 100% HTTP Transport (bd-930h6)

### Phase 1: Core Blockers (P1)

| ID | Task | Description |
|----|------|-------------|
| bd-c50kp | **Migrate session gates from NATS KV to DB + HTTP RPC** | Move `OpSessionGateMark/Check/Clear/List` from NATS KV to Dolt DB. Add HTTP method mappings. NATS KV can remain as cache for local daemons. |
| bd-xq90d | **Remove local-daemon NATS preference from watch/subscribe** | `bd watch` and `bd bus subscribe` currently prefer direct NATS for local daemons. Force HTTP SSE as default transport everywhere. Keep `--nats` as opt-in. |
| bd-h4sye | **HTTP-native event bus emit round-trip** | Verify full cycle: remote agent emits via HTTP POST → daemon dispatches handlers → publishes to JetStream → remote agent receives via SSE. Add ack/confirmation. |
| bd-v37ht | **Eliminate Unix socket as required transport** | When `BD_DAEMON_HTTP_URL` is set, client should NEVER fall back to Unix socket. Audit all `daemonClient.Execute()` call sites. Add HTTP-only integration test. |

### Phase 2: Completeness (P2)

| ID | Task | Description |
|----|------|-------------|
| bd-jk5ay | **Add missing HTTP method mappings** | 6 operations missing from `operationToHTTPMethod()`. Blocked by session gate migration. |
| bd-pm3yz | **SSE reconnection with Last-Event-ID** | Implement auto-reconnect with `Last-Event-ID` header per SSE spec. Critical for unreliable networks. |
| bd-us8f1 | **HTTP auth token forwarding for federation** | Extend per-rig API keys with agent identity validation, token refresh, and cross-daemon trust. |
| bd-nffa2 | **Integration test: full workflow over HTTP only** | End-to-end test: daemon with HTTP-only (no socket), full CRUD + SSE + gates + watch. Acceptance test for the epic. |

### Dependency Graph

```
bd-930h6 (Epic: 100% HTTP)
├── bd-c50kp (Session gates → DB)
│   └── bd-jk5ay (HTTP mappings)
│       └── bd-nffa2 (Integration test)
├── bd-xq90d (Remove NATS preference)
│   └── bd-nffa2
├── bd-h4sye (HTTP event emit)
├── bd-v37ht (HTTP-first client)
│   └── bd-nffa2
├── bd-pm3yz (SSE reconnection)
└── bd-us8f1 (Federation auth)
    └── bd-z7zbh (Cross-daemon events)
```

## Epic 2: Local↔Cloud Agent Interoperability (bd-89oz2)

*Depends on Epic 1 (bd-930h6)*

### Tasks

| ID | Task | Description |
|----|------|-------------|
| bd-z7zbh | **Cross-daemon event routing** | Local daemon subscribes to cloud daemon's SSE, republishes to local NATS/handlers. Hub-spoke model: cloud daemon is hub. |
| bd-fz1sw | **Shared issue visibility across daemons** | Option 1 (MVP): all agents point to cloud daemon via `BD_DAEMON_HOST`. Option 2 (later): Dolt bidirectional replication. |

### Interop Model

```
┌────────────────┐              ┌────────────────────┐
│ LOCAL MACHINE   │              │ KUBERNETES (gasboat)│
│                 │              │                     │
│ ┌─────────────┐│   HTTP/SSE   │┌───────────────────┐│
│ │ bd CLI      ├┼─────────────►││ bd daemon (cloud)  ││
│ │ (agent)     ││              ││                     ││
│ └─────────────┘│              ││ ┌─────────────────┐││
│                 │              ││ │ NATS JetStream  │││
│ ┌─────────────┐│              ││ └─────────────────┘││
│ │ bd daemon   ├┼──SSE sub────►││                     ││
│ │ (local)     ││              ││ ┌─────────────────┐││
│ └─────────────┘│              ││ │ Dolt DB         │││
│                 │              ││ └─────────────────┘││
└────────────────┘              │└───────────────────┘│
                                │                     │
                                │ ┌───────────────┐   │
                                │ │ agent pods     │   │
                                │ │ (K8s agents)   │   │
                                │ └───────────────┘   │
                                └────────────────────┘
```

## Key Design Decisions

### 1. SSE over WebSocket

We chose **Server-Sent Events (SSE)** over WebSocket because:
- SSE works through HTTP proxies, load balancers, and CDNs without special config
- SSE has built-in reconnection with `Last-Event-ID`
- SSE is unidirectional (server→client), matching our event streaming pattern
- Write operations use standard HTTP POST (no need for bidirectional socket)
- SSE is simpler to debug (plain text, curl-friendly)

### 2. Hub-Spoke over Peer-to-Peer

For cross-daemon communication, we chose **hub-spoke** (cloud daemon as hub) because:
- Single source of truth for issues and events
- No conflict resolution needed
- Local daemons are stateless event consumers
- Scales to many local agents without N² connections

### 3. NATS Remains Internal

NATS JetStream remains the **daemon-internal** event bus. It's never exposed to external clients:
- HTTP SSE is the external interface (proxies NATS events)
- This decouples clients from NATS protocol/version
- Allows swapping NATS for another event store without client changes

## HTTP API Surface (Complete)

### RPC Endpoints

```
POST /bd.v1.BeadsService/{Method}
Authorization: Bearer <token>
Content-Type: application/json
X-BD-Actor: <agent-name>

→ 159+ operations (Create, List, Update, Close, etc.)
```

### SSE Streaming

```
GET /events?since=<ms>&filter=type:<mutation-type>
GET /bus/events?stream=hooks,agents&filter=AgentStarted&since=<ms>
Authorization: Bearer <token>

→ Server-Sent Events (text/event-stream)
→ 10 JetStream streams: hooks, decisions, oj, agents, mail,
   mutations, config, gate, inbox, jack
```

### Health

```
GET /health    → {"status":"healthy","version":"...","uptime":"..."}
GET /livez     → {"status":"alive"}  (no DB check)
GET /readyz    → {"status":"ready"}  (accepts degraded)
GET /metrics   → {...}
```

## Execution Order

1. **bd-c50kp**: Session gates migration (unblocks everything)
2. **bd-v37ht**: HTTP-first client (foundational)
3. **bd-xq90d**: Remove NATS preference (quick win)
4. **bd-h4sye**: Verify HTTP event round-trip (validation)
5. **bd-jk5ay**: Missing HTTP mappings (cleanup)
6. **bd-pm3yz**: SSE reconnection (reliability)
7. **bd-nffa2**: Integration test (acceptance)
8. **bd-us8f1**: Federation auth (bridge to Epic 2)
9. **bd-z7zbh**: Cross-daemon events (Epic 2)
10. **bd-fz1sw**: Shared issue visibility (Epic 2)

## Files Reference

| File | Purpose |
|------|---------|
| `internal/rpc/http_server.go` | HTTP server setup, routing, auth, CORS |
| `internal/rpc/http_client.go` | HTTP client + operation→method mapping |
| `internal/rpc/http_client_sse.go` | SSE streaming clients |
| `internal/rpc/http_sse.go` | Server-side SSE for mutations |
| `internal/rpc/http_sse_bus.go` | Server-side SSE for all event streams |
| `internal/rpc/client.go` | Transport selection (socket vs HTTP) |
| `internal/rpc/auth.go` | Per-rig API key authorization |
| `internal/eventbus/streams.go` | JetStream stream + KV bucket definitions |
| `internal/eventbus/types.go` | Event types and payload structs |
| `cmd/bd/watch_await.go` | Watch transport selection logic |
| `cmd/bd/bus_subscribe.go` | Bus subscribe (SSE default, NATS opt-in) |
