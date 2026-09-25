# ADR 0004: Report-only reconciliation

- Status: accepted
- Date: 2026-09-26
- Scope: V1

## Context

StockFlow keeps an optimized balance row (`inventory_balances`) alongside an
append-only movement ledger. Two representations can drift: a bug, a manual
`UPDATE`, or an interrupted operator action can leave `reserved` inconsistent
with the active reservations, or a shipped line without its decrement. The
system needs a way to *detect and explain* such drift without a background job
silently rewriting history, because silent repair would:

- erase the evidence needed to understand the original defect;
- diverge from the movement ledger, which is the append-only authority;
- mask bugs that should be fixed at their source.

## Decision

`reconciliation` is strictly report-only.

- Checks run inside a `set transaction read only` transaction. No check may
  write to `inventory_balances`, `inventory_movements`, `orders`,
  `order_items`, or `reservations`.
- The run and its findings are persisted in a **separate** transaction to
  `reconciliation_runs` and `reconciliation_findings`, plus an `audit_entries`
  row. Findings are append-only (triggers reject `UPDATE`/`DELETE`).
- Every finding stores `check_name`, optional `sku_id`/`order_id`, an
  `entity_ref`, and non-blank `expected` and `observed` text so an operator can
  see exactly what was wrong.
- Checks implemented:
  `reserved_matches_active_reservations`,
  `balance_matches_movement_ledger` (includes a missing balance row),
  `terminal_orders_have_no_active_reservation`,
  `shipped_line_has_one_matching_decrement` (matching order, item, SKU, and
  quantity; V2), `manual_movement_has_actor_and_reason`, and
  `balance_values_respect_invariants`.
- The audit actor for a run is the authenticated operator, not a hard-coded
  string, so reconciliation is attributable like any other action.
- A run's status is `clean` or `findings`; the finding count is stored.
- `cmd/reconcile` exits `0` clean, `1` findings (unless
  `-fail-on-findings=false`), and `2` on an operational error.
- A discrepancy is injected for testing only by direct SQL in the integration
  tests and the env-gated `cmd/demo` fixture path
  (`STOCKFLOW_ALLOW_DEMO_FIXTURES=true`). There is no production endpoint that
  corrupts data.

Correcting a real discrepancy is a deliberate operator action via a
compensating movement, never something the reconciler does on its own.

## Alternatives rejected

- **Auto-repair by recomputing balances from the ledger:** destroys the
  evidence of the drift and can amplify a movement-ledger bug if the balance
  was in fact right.
- **A repair endpoint or repair mode:** makes accidental corruption a single
  API call away and hides the bug class.
- **Checks in the same transaction as the write of findings:** a failing check
  would roll back the findings that explain the failure; the read and the
  persistence are deliberately separated.
- **Background scheduled reconciler:** V1 is a single process with no broker;
  an explicit, observable run is enough and keeps the operator in control.
- **Returning only a boolean "consistent" flag:** expected/observed detail is
  required to act on a finding.

## Consequences

- Corruption is surfaced, never hidden: a finding names the entity, the
  expected value, and the observed value.
- Repeated runs are safe and independent; they add history rather than mutate
  state.
- The invariant checks are limited to the modelled properties above and are not
  a general accounting reconciliation.
- Because checks are read-only, running reconciliation cannot interfere with
  concurrent order, cancellation, or shipment transactions.
