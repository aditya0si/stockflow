# StockFlow — Inventory Reservation and Fulfilment Workbench

> Implementation contract. This is a small, trustworthy commerce system—not a miniature marketplace.

## A. Spec critique

The old spec was at least three projects: storefront, warehouse PWA, and operations platform. Multi-warehouse allocation, returns, offline writes, Redis, SSE, an outbox, a DLQ, Redpanda, and a traffic simulator existed mainly to expose technologies. The fictional spreadsheet retailer did not justify them. Targets such as 500 orders/s and 200 live clients were unrelated to the proposed user and therefore were not credible product evidence. The broad product surface also avoided the hard domain questions: atomic multi-SKU reservation, ambiguous client retries, cancellation-versus-shipment races, and explainable inventory discrepancies.

**Decision:** retain the inventory idea, delete the marketplace/platform theatre, and center the repository on transaction correctness and auditability.

## B. Project identity

### User
One owner/operator and a few fulfilment workers managing a single stock pool from one physical location.

### Problem
A spreadsheet can record a balance but cannot safely decide which of two simultaneous orders gets the last unit, explain every adjustment, or recover cleanly when a client times out after a successful write.

### Why existing solutions are insufficient
Commercial inventory systems already exist; StockFlow does not claim a market gap. It is appropriate for a small seller who needs a transparent, self-hosted workflow, and as an engineering investigation of reservation correctness. If no real seller validates it, the README must call it an engineering case study rather than imply adoption.

### Personal motivation
Use only a truthful origin. Default: “I wanted to understand why apparently simple checkout inventory becomes unsafe under concurrent requests and how to make every balance explainable.” Do not invent a family-business story.

### Central engineering question
**How can an order API reserve limited stock atomically, remain safe under retries and races, and preserve enough evidence to explain or detect every discrepancy?**

## C. Revised product scope

Core workflow:
1. Create/import SKUs and opening stock.
2. Receive stock or make a reasoned manual adjustment.
3. Submit an all-or-nothing order with an idempotency key.
4. Pick, pack, and ship an accepted order.
5. Cancel before shipment and release its reservation once.
6. Inspect append-only inventory movements and audit history.
7. Run reconciliation to report inconsistencies without silently repairing them.

Initial release supports one location, one currency, one operator UI, no payments, no partial fulfilment, no substitutions, and no returns.

## D. Technical architecture

A Go modular monolith, PostgreSQL, and a small React/TypeScript operator UI. Docker Compose runs the application and database. Modules:

- `catalog`: SKU identity and metadata
- `inventory`: balances, receipts, adjustments, immutable movements
- `orders`: creation, idempotency, reservation, cancellation
- `fulfilment`: pick/pack/ship state machine
- `audit`: actor/action history and reconciliation findings

Core tables: `skus`, `inventory_balances`, `inventory_movements`, `orders`, `order_items`, `reservations`, `idempotency_records`, `fulfilment_events`, `audit_entries`, `reconciliation_runs`, and `reconciliation_findings`.

One process and one database are the deployable architecture. Polling is sufficient for the first UI. No broker, cache, microservices, or Kubernetes.

## E. Decision rationale

### Atomic reservation
**Problem:** multiple buyers can observe the last unit. **Constraint:** all order lines succeed or none do. **Options:** optimistic versioning, serializable transactions, row locks. **Decision:** normalize duplicate SKU lines, sort SKU IDs, lock balance rows with `SELECT … FOR UPDATE`, validate all lines, then write the order/reservations/movements in one transaction. **Trade-off:** hot SKUs serialize; the behavior is simple to inspect and deadlock risk is reduced by stable lock ordering.

### PostgreSQL as the only authority
**Problem:** durable stock must remain coherent. **Constraint:** the system is small and transactional. **Options:** PostgreSQL only, Redis reservation counters, event sourcing. **Decision:** PostgreSQL constraints, rows, and history tables. **Trade-off:** database availability bounds ordering availability; there is no second source of truth to reconcile.

### Balance plus movement ledger
**Problem:** recalculating every balance from history is expensive, while a mutable balance alone is unauditable. **Decision:** keep an optimized balance row and append-only movements; reconcile the two. **Trade-off:** two representations require invariants and tests. This is not claimed as full event sourcing or accounting.

### Modular monolith
A reservation crosses inventory and ordering in one transaction. Network boundaries would create distributed failure without a demonstrated scaling need. Module boundaries live in code and tests.

### Idempotency
Store caller scope, key, canonical request hash, state, and the definitive response. Same key/same payload returns the original outcome; same key/different payload returns `409`. This promises a stable committed API outcome, not “exactly-once delivery.”

## F. Deep engineering areas

1. **Concurrency and transaction design:** lock order, hot-row contention, constraint design, deadlock retries, and multi-SKU rollback.
2. **Idempotency under ambiguous outcomes:** concurrent duplicate requests, process death after commit, payload conflicts, and abandoned `in_progress` records.
3. **Auditability and reconciliation:** deriving expected balances, detecting impossible state, and using compensating movements instead of history edits.

Core invariants:
- `available = on_hand - reserved`
- `on_hand >= 0`, `reserved >= 0`, `available >= 0`
- one active reservation per order item
- shipped/cancelled orders have no active reservation
- one stock decrement per shipment
- every manual adjustment has actor and reason

## G. Engineering evolution

### V0 — executable domain spike
Schema, migrations, receipts, atomic reservation, cancellation, HTTP/CLI, and real-PostgreSQL contention tests. Exit: fifty buyers cannot oversell one unit.

### V1 — useful product
Operator UI, SKU/stock administration, pick-pack-ship, payload-aware idempotency, movement history, reconciliation, deterministic seed, CI, and one-command startup. Exit: a new reviewer completes the documented workflow.

### V2 — measured hardening
Role distinction, indexed/paginated queries, structured logs and correlation IDs, backup/restore rehearsal, accessibility pass, race/fault tests, and committed benchmark artifacts. ADRs record the reservation and idempotency decisions.

### V3 — one evidence-driven extension
Choose one only: returns, a second location, notification outbox, barcode input, or SSE. Add it only after real use or measurement establishes the need.

## H. Failure model

- Two orders compete for the final unit: one commits; others get a domain conflict.
- Multi-SKU order contains an unavailable line: entire transaction rolls back.
- Client loses response after commit: same idempotency key returns the committed order.
- Same key with changed payload: `409`, no mutation.
- Database disconnect before commit: no partial order/reservation/balance change.
- Process dies after commit: persisted outcome remains discoverable.
- Cancellation races shipment: conditional transition lets one win; loser receives a state conflict; stock changes once.
- Duplicate shipment command: no second decrement.
- Reconciliation detects corruption: create a finding; never silently rewrite history.
- UI disconnect: reload authoritative server state; no offline mutation queue.

Only recognized transient database failures receive bounded jittered retries.

## I. Testing strategy

Unit tests cover transition and quantity rules. PostgreSQL integration tests cover transactions and constraints. API contract tests cover errors/idempotency. Playwright covers receipt → order → fulfilment. Property tests generate legal/illegal command sequences and assert inventory invariants. Load tests distinguish ordinary traffic from hot-SKU contention.

Five interviewer-worthy tests:
1. **One unit, fifty simultaneous buyers:** exactly one accepted order and no negative balance.
2. **Commit then dropped response:** retry returns the original order without another reservation.
3. **Multi-SKU rollback:** one unavailable line leaves every SKU and table unchanged.
4. **Cancellation versus shipment:** one valid terminal transition and one stock effect.
5. **Deliberate corruption:** reconciliation reports SKU, expected/observed values, and evidence without repairing it.

Do not mock PostgreSQL for concurrency claims.

## J. Performance/benchmark plan

Product-relevant scenarios: normal 1–5-line orders, hot-SKU contention, large multi-SKU orders, reconciliation over growing history, and paginated operations queries. Budgets guide work but are not claims: p95 <300 ms for a typical local order, 50 requests/s for ten minutes without unexpected errors, and reconciliation of 100,000 movements within 10 seconds on documented hardware.

Commit generator seed, hardware/software, exact commands, raw output, query plans, baseline, changed implementation, and causal explanation. Report misses honestly; never extrapolate to marketplace scale.

## K. Deployment/demo strategy

`docker compose up --build` plus one reset/seed command. Public demo is optional; a recorded fallback is required if hosting sleeps. Demo credentials contain no real data.

Seven-minute proof: show one remaining unit; launch concurrent orders; inspect the sole winner; replay its idempotency key; pick/pack/ship; inspect movements; inject a test-only discrepancy; run reconciliation; show that no silent repair occurred. README includes health check and sample `curl` requests.

## L. Repository structure

```text
stockflow/
├── cmd/{api,reconcile}/
├── internal/{catalog,inventory,orders,fulfilment,audit}/
├── internal/platform/{postgres,http,observability}/
├── migrations/
├── web/{src,e2e}/
├── tests/{integration,contention,load}/
├── benchmarks/{scripts,raw,reports}/
├── docs/{domain.md,failure-model.md,demo.md,adr/}
├── compose.yaml
├── Makefile
└── README.md
```

README order: what/why, 60-second demo, engineering questions, architecture, decisions, local run, tests, benchmarks, limitations, future work. No badge wall.

## M. Scope control

### MUST BUILD
Atomic all-or-nothing reservation; database invariants; payload-aware idempotency; legal fulfilment transitions; immutable movements; report-only reconciliation; usable operator workflow; real PostgreSQL tests; deterministic demo; evidence-backed claims.

### SHOULD BUILD
Structured errors/logging, correlation IDs, pagination/indexes, actor/reason audit, backup/restore drill, keyboard/mobile usability, query-plan notes.

### OPTIONAL
One V3 extension, hosted demo, generated API docs kept in sync.

### DO NOT BUILD
Storefront, payments, multiple warehouses, allocation strategies, Redis inventory, Kafka/Redpanda, microservices, Kubernetes, offline writes, AI, fake operational dashboards, “exactly once,” or synthetic global-scale claims.

## N. Known limitations

Single location; all-or-nothing orders; no payments/delivery/returns/marketplace sync; demo-grade auth until separately hardened; database is a single availability boundary; reconciliation detects only modeled inconsistencies; local benchmarks do not predict large-retailer capacity. At 10×, tune indexes/pool/partition history from measurements. At 100× or external consumers, evaluate read replicas and an outbox before considering service splits.

## O. Natural JD relevance

Only after implementation:
- **Core:** Go, REST, PostgreSQL transactions/schema design, concurrency, idempotency, state modeling, testing/debugging, commerce concepts.
- **Supporting:** React/TypeScript, Docker, CI, logs/metrics, accessibility, load testing.
- **Demonstrated elsewhere:** streaming pipelines, Redis, DLQs, backpressure, resumable SSE.
- **Not worth forcing:** microservices, Kubernetes, recommendations/ML, multi-region consistency, GraphQL, event sourcing.

## P. Completion checklist

- [ ] Fresh migrations and one-command startup work.
- [ ] Receipt → order → pick → pack → ship works in the UI.
- [ ] Database constraints encode non-negative stock.
- [ ] Duplicate lines normalize and multi-SKU reservation is atomic.
- [ ] Same-key payload conflict and ambiguous retry behavior are proven.
- [ ] Shipment/cancellation race and the five named tests pass in CI.
- [ ] Reconciliation detects seeded corruption without repair.
- [ ] Benchmarks include raw results, environment, and commands.
- [ ] Demo reset, health check, sample requests, screenshots/video work.
- [ ] ADRs explain rejected alternatives and known limits.
- [ ] No README/CV number lacks a reproducible artifact.
- [ ] No unfinished headline feature appears on the CV.
- [ ] The author can explain every transaction boundary without generated notes.

**CV gate:** do not list StockFlow until all MUST items, the five tests, demo, README, and evidence artifacts are complete.