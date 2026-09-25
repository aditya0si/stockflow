import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, errorMessage, newIdempotencyKey } from './api'
import type {
  BalanceRow,
  FulfilmentEvent,
  Movement,
  Order,
  OrderStatus,
  ReconciliationRun,
} from './types'

type Tab = 'stock' | 'orders' | 'reconciliation'
type Notice = { kind: 'ok' | 'error'; text: string } | null

interface OrderDetail {
  order: Order
  events: FulfilmentEvent[]
  movements: Movement[]
}

const statusOrder: OrderStatus[] = ['accepted', 'picking', 'packed', 'shipped', 'cancelled']

function nextAction(status: OrderStatus): 'pick' | 'pack' | 'ship' | null {
  switch (status) {
    case 'accepted':
      return 'pick'
    case 'picking':
      return 'pack'
    case 'packed':
      return 'ship'
    default:
      return null
  }
}

function cancancellable(status: OrderStatus): boolean {
  return status === 'accepted' || status === 'picking' || status === 'packed'
}

export default function App() {
  const [tab, setTab] = useState<Tab>('stock')
  const [notice, setNotice] = useState<Notice>(null)
  const [busy, setBusy] = useState(false)

  const [balances, setBalances] = useState<BalanceRow[]>([])
  const [orders, setOrders] = useState<Order[]>([])
  const [selectedOrderId, setSelectedOrderId] = useState<string | null>(null)
  const [orderDetail, setOrderDetail] = useState<OrderDetail | null>(null)
  const [runs, setRuns] = useState<ReconciliationRun[]>([])
  const [selectedRun, setSelectedRun] = useState<ReconciliationRun | null>(null)

  const refreshBalances = useCallback(async () => {
    setBalances(await api.listBalances())
  }, [])

  const refreshOrders = useCallback(async () => {
    setOrders(await api.listOrders())
  }, [])

  const refreshRuns = useCallback(async () => {
    setRuns(await api.listReconciliationRuns())
  }, [])

  const loadOrder = useCallback(async (id: string) => {
    const [order, events, movements] = await Promise.all([
      api.getOrder(id),
      api.orderEvents(id),
      api.movements({ order_id: id }),
    ])
    setOrderDetail({ order, events, movements })
  }, [])

  useEffect(() => {
    void refreshBalances().catch((error) => setNotice({ kind: 'error', text: errorMessage(error) }))
    void refreshOrders().catch((error) => setNotice({ kind: 'error', text: errorMessage(error) }))
    void refreshRuns().catch((error) => setNotice({ kind: 'error', text: errorMessage(error) }))
  }, [refreshBalances, refreshOrders, refreshRuns])

  const runAction = async (action: () => Promise<unknown>, successText: string) => {
    setBusy(true)
    setNotice(null)
    try {
      await action()
      setNotice({ kind: 'ok', text: successText })
    } catch (error) {
      setNotice({ kind: 'error', text: errorMessage(error) })
    } finally {
      setBusy(false)
    }
  }

  const selectOrder = (id: string) => {
    setSelectedOrderId(id)
    void loadOrder(id).catch((error) => setNotice({ kind: 'error', text: errorMessage(error) }))
  }

  const selected = selectedOrderId
  const stockCodes = useMemo(() => balances.map((b) => b.code), [balances])

  return (
    <div className="app">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="app-header">
        <h1>StockFlow operator</h1>
        <p className="subtitle">
          Single-location inventory reservation and fulfilment. No offline writes.
        </p>
      </header>

      <nav aria-label="Sections">
        <div className="tabs" role="tablist">
          {(
            [
              ['stock', 'Stock'],
              ['orders', 'Orders'],
              ['reconciliation', 'Reconciliation'],
            ] as Array<[Tab, string]>
          ).map(([value, label]) => (
            <button
              key={value}
              role="tab"
              id={`tab-${value}`}
              aria-selected={tab === value}
              aria-controls={`panel-${value}`}
              className={tab === value ? 'tab active' : 'tab'}
              onClick={() => setTab(value)}
            >
              {label}
            </button>
          ))}
        </div>
      </nav>

      {notice && (
        <p
          className={notice.kind === 'error' ? 'notice error' : 'notice ok'}
          role={notice.kind === 'error' ? 'alert' : 'status'}
          aria-live="polite"
        >
          {notice.text}
        </p>
      )}

      <main id="main">
        {tab === 'stock' && (
          <section id="panel-stock" role="tabpanel" aria-labelledby="tab-stock">
            <StockPanel
              balances={balances}
              busy={busy}
              onRefresh={refreshBalances}
              onNotice={setNotice}
              onAction={runAction}
            />
          </section>
        )}

        {tab === 'orders' && (
          <section id="panel-orders" role="tabpanel" aria-labelledby="tab-orders">
            <OrdersPanel
              orders={orders}
              stockCodes={stockCodes}
              busy={busy}
              selectedId={selected}
              detail={orderDetail}
              onSelect={selectOrder}
              onRefresh={async () => {
                await refreshOrders()
                if (selected) {
                  await loadOrder(selected)
                }
              }}
              onAction={runAction}
            />
          </section>
        )}

        {tab === 'reconciliation' && (
          <section id="panel-reconciliation" role="tabpanel" aria-labelledby="tab-reconciliation">
            <ReconciliationPanel
              runs={runs}
              busy={busy}
              selectedRun={selectedRun}
              onSelect={async (id) => {
                try {
                  setSelectedRun(await api.getReconciliationRun(id))
                } catch (error) {
                  setNotice({ kind: 'error', text: errorMessage(error) })
                }
              }}
              onRun={async () => {
                await runAction(async () => {
                  const run = await api.runReconciliation()
                  setSelectedRun(run)
                  await refreshRuns()
                }, 'Reconciliation run completed (report only)')
              }}
            />
          </section>
        )}
      </main>
    </div>
  )
}

interface StockProps {
  balances: BalanceRow[]
  busy: boolean
  onRefresh: () => Promise<void>
  onNotice: (notice: Notice) => void
  onAction: (action: () => Promise<unknown>, successText: string) => Promise<void>
}

function StockPanel({ balances, busy, onRefresh, onNotice, onAction }: StockProps) {
  const [receive, setReceive] = useState({ sku: '', quantity: 1, actor: 'operator', reason: '' })
  const [sku, setSku] = useState({
    code: '',
    name: '',
    actor: 'operator',
    opening_quantity: 0,
    opening_reason: '',
  })

  return (
    <div className="grid">
      <div className="card">
        <div className="card-head">
          <h2>SKUs and balances</h2>
          <button
            type="button"
            onClick={() => {
              onRefresh().catch((error) => onNotice({ kind: 'error', text: errorMessage(error) }))
            }}
          >
            Refresh
          </button>
        </div>
        <div className="table-wrap">
          <table>
            <caption className="sr-only">SKU balances</caption>
            <thead>
              <tr>
                <th scope="col">Code</th>
                <th scope="col">Name</th>
                <th scope="col" className="num">
                  On hand
                </th>
                <th scope="col" className="num">
                  Reserved
                </th>
                <th scope="col" className="num">
                  Available
                </th>
              </tr>
            </thead>
            <tbody>
              {balances.length === 0 && (
                <tr>
                  <td colSpan={5}>No SKUs yet. Create one on the right.</td>
                </tr>
              )}
              {balances.map((row) => (
                <tr key={row.sku_id}>
                  <th scope="row">{row.code}</th>
                  <td>{row.name}</td>
                  <td className="num">{row.on_hand}</td>
                  <td className="num">{row.reserved}</td>
                  <td className={`num ${row.available === 0 ? 'zero' : ''}`}>{row.available}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="card">
        <h2>Receive stock</h2>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void onAction(
              () => api.receive(receive),
              `Received ${receive.quantity} of ${receive.sku}`,
            ).then(() => {
              setReceive({ sku: '', quantity: 1, actor: 'operator', reason: '' })
              void onRefresh()
            })
          }}
        >
          <label htmlFor="receive-sku">SKU code</label>
          <input
            id="receive-sku"
            required
            value={receive.sku}
            onChange={(event) => setReceive({ ...receive, sku: event.target.value })}
            list="sku-codes"
          />
          <datalist id="sku-codes">
            {balances.map((row) => (
              <option key={row.sku_id} value={row.code} />
            ))}
          </datalist>

          <label htmlFor="receive-quantity">Quantity</label>
          <input
            id="receive-quantity"
            type="number"
            min={1}
            required
            value={receive.quantity}
            onChange={(event) => setReceive({ ...receive, quantity: Number(event.target.value) })}
          />

          <label htmlFor="receive-actor">Actor</label>
          <input
            id="receive-actor"
            required
            value={receive.actor}
            onChange={(event) => setReceive({ ...receive, actor: event.target.value })}
          />

          <label htmlFor="receive-reason">Reason</label>
          <input
            id="receive-reason"
            required
            value={receive.reason}
            onChange={(event) => setReceive({ ...receive, reason: event.target.value })}
          />

          <button type="submit" disabled={busy}>
            Receive
          </button>
        </form>

        <h2>Create SKU</h2>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void onAction(() => api.createSku(sku), `Created ${sku.code}`).then(() => {
              setSku({
                code: '',
                name: '',
                actor: 'operator',
                opening_quantity: 0,
                opening_reason: '',
              })
              void onRefresh()
            })
          }}
        >
          <label htmlFor="sku-code">Code</label>
          <input
            id="sku-code"
            required
            value={sku.code}
            onChange={(event) => setSku({ ...sku, code: event.target.value })}
          />

          <label htmlFor="sku-name">Name</label>
          <input
            id="sku-name"
            required
            value={sku.name}
            onChange={(event) => setSku({ ...sku, name: event.target.value })}
          />

          <label htmlFor="sku-actor">Actor</label>
          <input
            id="sku-actor"
            required
            value={sku.actor}
            onChange={(event) => setSku({ ...sku, actor: event.target.value })}
          />

          <label htmlFor="sku-opening">Opening quantity</label>
          <input
            id="sku-opening"
            type="number"
            min={0}
            value={sku.opening_quantity}
            onChange={(event) => setSku({ ...sku, opening_quantity: Number(event.target.value) })}
          />

          <label htmlFor="sku-opening-reason">Opening reason</label>
          <input
            id="sku-opening-reason"
            value={sku.opening_reason}
            onChange={(event) => setSku({ ...sku, opening_reason: event.target.value })}
          />

          <button type="submit" disabled={busy}>
            Create SKU
          </button>
        </form>
      </div>
    </div>
  )
}

interface OrdersProps {
  orders: Order[]
  stockCodes: string[]
  busy: boolean
  selectedId: string | null
  detail: OrderDetail | null
  onSelect: (id: string) => void
  onRefresh: () => Promise<void>
  onAction: (action: () => Promise<unknown>, successText: string) => Promise<void>
}

function OrdersPanel({
  orders,
  stockCodes,
  busy,
  selectedId,
  detail,
  onSelect,
  onRefresh,
  onAction,
}: OrdersProps) {
  const [lines, setLines] = useState<Array<{ sku: string; quantity: number }>>([
    { sku: '', quantity: 1 },
  ])
  const [scope, setScope] = useState('operator')

  const submitOrder = async () => {
    const cleaned = lines
      .map((line) => ({ sku: line.sku.trim().toUpperCase(), quantity: line.quantity }))
      .filter((line) => line.sku !== '')
    await onAction(
      () => api.createOrder(scope, newIdempotencyKey(), cleaned),
      'Order submitted',
    ).then(() => {
      setLines([{ sku: '', quantity: 1 }])
      void onRefresh()
    })
  }

  const order = detail?.order ?? null

  return (
    <div className="grid">
      <div className="card">
        <h2>Submit order</h2>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void submitOrder()
          }}
        >
          <label htmlFor="order-scope">Caller scope</label>
          <input
            id="order-scope"
            required
            value={scope}
            onChange={(event) => setScope(event.target.value)}
          />

          <datalist id="order-sku-codes">
            {stockCodes.map((code) => (
              <option key={code} value={code} />
            ))}
          </datalist>

          {lines.map((line, index) => (
            <div className="line-row" key={index}>
              <div>
                <label htmlFor={`line-sku-${index}`}>SKU {index + 1}</label>
                <input
                  id={`line-sku-${index}`}
                  required
                  list="order-sku-codes"
                  value={line.sku}
                  onChange={(event) => {
                    const next = [...lines]
                    next[index] = { ...next[index], sku: event.target.value }
                    setLines(next)
                  }}
                />
              </div>
              <div>
                <label htmlFor={`line-qty-${index}`}>Quantity</label>
                <input
                  id={`line-qty-${index}`}
                  type="number"
                  min={1}
                  required
                  value={line.quantity}
                  onChange={(event) => {
                    const next = [...lines]
                    next[index] = { ...next[index], quantity: Number(event.target.value) }
                    setLines(next)
                  }}
                />
              </div>
              <button
                type="button"
                className="secondary"
                onClick={() => setLines(lines.filter((_, i) => i !== index))}
                disabled={lines.length === 1}
              >
                Remove
              </button>
            </div>
          ))}

          <div className="row">
            <button
              type="button"
              className="secondary"
              onClick={() => setLines([...lines, { sku: '', quantity: 1 }])}
            >
              Add line
            </button>
            <button type="submit" disabled={busy}>
              Submit order
            </button>
          </div>
        </form>

        <div className="card-head">
          <h2>Orders</h2>
          <button
            type="button"
            onClick={() => {
              void onRefresh()
            }}
          >
            Refresh
          </button>
        </div>
        <ul className="order-list">
          {orders.length === 0 && <li>No orders yet.</li>}
          {orders.map((o) => (
            <li key={o.id}>
              <button
                type="button"
                className={o.id === selectedId ? 'order-link selected' : 'order-link'}
                aria-current={o.id === selectedId}
                onClick={() => onSelect(o.id)}
              >
                <span className={`status ${o.status}`}>{o.status}</span>
                <span className="mono">{o.id.slice(0, 8)}</span>
                <span>{(o.items ?? []).reduce((sum, item) => sum + item.quantity, 0)} units</span>
              </button>
            </li>
          ))}
        </ul>
      </div>

      <div className="card">
        <h2>Order detail</h2>
        {!order && <p>Select an order to see its state and history.</p>}
        {order && (
          <>
            <p>
              <span className={`status ${order.status}`}>{order.status}</span>{' '}
              <span className="mono">{order.id}</span>
            </p>
            <div className="row">
              {nextAction(order.status) && (
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => {
                    const action = nextAction(order.status)!
                    void onAction(
                      () => api.transition(order.id, action, 'operator', `ui ${action}`),
                      `Order moved to next state (${action})`,
                    ).then(() => onRefresh())
                  }}
                >
                  {nextAction(order.status)}
                </button>
              )}
              {cancancellable(order.status) && (
                <button
                  type="button"
                  className="danger"
                  disabled={busy}
                  onClick={() => {
                    void onAction(
                      () => api.cancelOrder(order.id, 'operator', 'cancelled from UI'),
                      'Order cancelled',
                    ).then(() => onRefresh())
                  }}
                >
                  Cancel
                </button>
              )}
              {!nextAction(order.status) && !cancancellable(order.status) && (
                <span className="muted">Terminal state</span>
              )}
            </div>

            <h3>Items</h3>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th scope="col">SKU</th>
                    <th scope="col" className="num">
                      Quantity
                    </th>
                    <th scope="col">Reservation</th>
                  </tr>
                </thead>
                <tbody>
                  {(order.items ?? []).map((item) => (
                    <tr key={item.id}>
                      <th scope="row">{item.sku}</th>
                      <td className="num">{item.quantity}</td>
                      <td>{item.reservation_status}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <h3>Movements</h3>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th scope="col">Kind</th>
                    <th scope="col" className="num">
                      On hand
                    </th>
                    <th scope="col" className="num">
                      Reserved
                    </th>
                    <th scope="col">Actor</th>
                    <th scope="col">Reason</th>
                  </tr>
                </thead>
                <tbody>
                  {(detail?.movements ?? []).map((movement) => (
                    <tr key={movement.id}>
                      <th scope="row">{movement.kind}</th>
                      <td className="num">{movement.delta_on_hand}</td>
                      <td className="num">{movement.delta_reserved}</td>
                      <td>{movement.actor}</td>
                      <td>{movement.reason ?? ''}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <h3>Fulfilment events</h3>
            <ol className="events">
              {(detail?.events ?? []).map((event) => (
                <li key={event.id}>
                  {event.from_status} &rarr; <strong>{event.to_status}</strong> by {event.actor}
                  {event.reason ? ` (${event.reason})` : ''}
                </li>
              ))}
              {(detail?.events ?? []).length === 0 && <li>No fulfilment events yet.</li>}
            </ol>
          </>
        )}
      </div>
    </div>
  )
}

interface ReconciliationProps {
  runs: ReconciliationRun[]
  busy: boolean
  selectedRun: ReconciliationRun | null
  onSelect: (id: string) => Promise<void>
  onRun: () => Promise<void>
}

function ReconciliationPanel({ runs, busy, selectedRun, onSelect, onRun }: ReconciliationProps) {
  return (
    <div className="grid">
      <div className="card">
        <div className="card-head">
          <h2>Reconciliation</h2>
          <button type="button" disabled={busy} onClick={() => void onRun()}>
            Run report
          </button>
        </div>
        <p className="muted">
          Report only: findings never repair data. Use a compensating movement to correct stock.
        </p>
        <ul className="run-list">
          {runs.length === 0 && <li>No runs yet.</li>}
          {runs.map((run) => (
            <li key={run.id}>
              <button
                type="button"
                className={selectedRun?.id === run.id ? 'order-link selected' : 'order-link'}
                onClick={() => void onSelect(run.id)}
              >
                <span className={`status ${run.status}`}>{run.status}</span>
                <span>{new Date(run.started_at).toLocaleString()}</span>
                <span>{run.findings_count} findings</span>
              </button>
            </li>
          ))}
        </ul>
      </div>

      <div className="card">
        <h2>Findings</h2>
        {!selectedRun && <p>Select a run to inspect its findings.</p>}
        {selectedRun && (
          <>
            <p>
              <span className={`status ${selectedRun.status}`}>{selectedRun.status}</span> —{' '}
              {selectedRun.checks_run} checks, {selectedRun.findings_count} findings
            </p>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th scope="col">Check</th>
                    <th scope="col">Reference</th>
                    <th scope="col">Expected</th>
                    <th scope="col">Observed</th>
                  </tr>
                </thead>
                <tbody>
                  {(selectedRun.findings ?? []).length === 0 && (
                    <tr>
                      <td colSpan={4}>No findings.</td>
                    </tr>
                  )}
                  {(selectedRun.findings ?? []).map((finding) => (
                    <tr key={finding.id}>
                      <th scope="row">{finding.check_name}</th>
                      <td>{finding.entity_ref ?? finding.sku_id ?? ''}</td>
                      <td>{finding.expected}</td>
                      <td>{finding.observed}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

export { statusOrder }
