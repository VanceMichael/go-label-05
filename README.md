# HerdCycle

HerdCycle is a PostgreSQL-backed smart ranch operations service inspired by the China News Service report on Minqin smart cattle feeding and manure-to-fertilizer recovery. It is a backend workspace for ranch managers, nutrition and equipment operators, and environmental officers.

The primary workflow connects feed planning to manure recovery:

1. A manager schedules a feed plan for an active cattle group and assigns an operator.
2. Feed inventory is reserved in the same transaction as the plan, audit event, and outbox event.
3. An operator completes the plan with an idempotency key and optimistic version.
4. Completion records the feed round, releases the reservation, creates a manure batch, and emits audit/outbox records atomically.
5. An environmental officer inspects the batch and approves a calculated compost lot.

Additional domain packages cover cattle health, nutrition formulation, feed-lot allocation and ledger entries, equipment scheduling, maintenance, quality inspection, transport, telemetry, reporting, recovery snapshots, audit hash chains, retry policy, business calendars, and batch partial-failure results.

## Runtime

- Go 1.22
- PostgreSQL 16
- `pgx/v5`
- Opaque, server-side sessions stored as SHA-256 token digests
- bcrypt password hashes

The HTTP server includes structured logging, request IDs, panic recovery, graceful shutdown, liveness and dependency readiness checks. Error responses have a stable `code`, a readable `message`, and a `request_id`.

## Local setup

Start PostgreSQL and the service:

```bash
docker compose up -d postgres
cp .env.example .env
set -a
source .env
set +a
GOTOOLCHAIN=local go run ./cmd/server
```

The default address is `127.0.0.1:8080`. The task-local PostgreSQL port is `55432`.

Development accounts are inserted only when their IDs do not already exist:

| Role | Email | Password |
| --- | --- | --- |
| Ranch manager | `manager@herd.local` | `manager-pass` |
| Equipment operator | `operator@herd.local` | `operator-pass` |
| Environmental officer | `environment@herd.local` | `environment-pass` |

These credentials are local bootstrap data, not production secrets. Change the bootstrap mechanism before deploying outside a development environment.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Process liveness |
| `GET` | `/readyz` | PostgreSQL readiness |
| `POST` | `/api/login` | Create a revocable, expiring session |
| `POST` | `/api/logout` | Revoke the current session |
| `POST` | `/api/feed-plans` | Manager schedules a plan |
| `GET` | `/api/feed-plans` | Paginated and status-filtered plan list |
| `POST` | `/api/feed-plans/complete` | Operator or manager completes a plan idempotently |
| `POST` | `/api/manure-batches/inspect` | Environmental officer records inspection |
| `POST` | `/api/manure-batches/approve` | Environmental officer or manager approves compost output |
| `GET` | `/api/manure-batches` | Paginated and status-filtered manure list |

Send the login token as `Authorization: Bearer <token>`. Missing, expired, revoked, unknown, or disabled-account sessions return `401`. A valid user without the required business role receives `403`.

## Migrations

Migrations are embedded from `migrations/*.sql`, ordered by numeric prefix, and recorded in `schema_migrations`. Startup takes a PostgreSQL transaction-scoped advisory lock, applies each missing version in a serializable transaction, and leaves applied files immutable.

- `001_init.sql`: identity, sessions, ranch groups, feed plans and rounds, manure/compost, idempotency, audit, and outbox tables.
- `002_inventory_and_outbox_claims.sql`: feed lots/reservations/ledger and lease metadata for concurrent outbox workers.

An existing database at version 1 receives version 2 on restart. Re-running migrations is idempotent because recorded versions are skipped. A failed migration is rolled back and is not recorded.

## Transactions and concurrency

- Feed scheduling reserves aggregate inventory and writes the plan, audit, and outbox event in one serializable transaction.
- Feed completion locks the plan, checks the expected version, enforces delivery tolerance, and creates the round and manure batch atomically.
- Inventory lots use row locks and earliest-expiry ordering for allocation.
- Manure inspection and approval use row locks plus optimistic versions.
- The outbox worker claims rows with `FOR UPDATE SKIP LOCKED`, commits a `running` lease before publishing, and protects settlement with `locked_by` ownership. Stale claims can be recovered after a configurable timeout.
- Idempotency keys reject changed requests and return the originally persisted workflow result for an exact replay.

## Verification

Database integration tests create isolated temporary PostgreSQL databases. PostgreSQL must be running before the test commands:

```bash
make postgres
make verify
```

The individual gates are:

```bash
GOTOOLCHAIN=local go test ./... -count=1
GOTOOLCHAIN=local go test -race ./... -count=1
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go build ./...
```

Tests cover migrations and reopen recovery, transaction rollback, idempotency, concurrent completion, inventory capacity, pagination and filters, HTTP parsing and error codes, login/logout/expiry/disabled accounts, role authorization, the full feed-to-compost path, and outbox retry/dead/cancel/stale-lease behavior.

## Container image

Build the service image with:

```bash
docker build -t herdcycle:local .
```

The multi-stage Dockerfile compiles a static binary with the Go 1.22 toolchain and runs it in a distroless image. Supply `DATABASE_URL` and optional `FARM_*` settings at runtime.
