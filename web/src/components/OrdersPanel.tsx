import { useState, type FormEvent } from 'react'
import { api, errorMessage, newIdempotencyKey } from '../api'
import type { Order, OrderDetail, OrderStatus } from '../types'
import type { Notice } from './Notice'
import type { RunAction } from './StockPanel'

interface OrdersPanelProps {
  orders: Order[]
  stockCodes: string[]
  busy: boolean
  selectedId: string | null
  detail: OrderDetail | null
  onSelect: (id: string) => void
  onRefresh: () => Promise<void>
  onNotice: (notice: Notice) => void
  onAction: RunAction
}

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

function cancellable(status: OrderStatus): boolean {
  return status === 'accepted' || status === 'picking' || status === 'packed'
}

const emptyLines = [{ sku: '', quantity: 1 }]

export function OrdersPanel({
  orders,
  stockCodes,
  busy,
  selectedId,
  detail,
  onSelect,
  onRefresh,
  onNotice,
  onAction,
}: OrdersPanelProps) {
  const [lines, setLines] = useState(emptyLines)

  const refresh = async () => {
    try {
      await onRefresh()
    } catch (error) {
      onNotice({ kind: 'error', text: errorMessage(error) })
    }
  }

  const submitOrder = async (event: FormEvent) => {
    event.preventDefault()
    const cleaned = lines
      .map((line) => ({ sku: line.sku.trim().toUpperCase(), quantity: line.quantity }))
      .filter((line) => line.sku !== '')
    try {
      await onAction(() => api.createOrder(newIdempotencyKey(), cleaned), 'Order submitted')
    } catch {
      // Keep the entered lines so the operator can adjust and resubmit.
      return
    }
    setLines(emptyLines)
    await refresh()
  }

  const order = detail?.order ?? null

  const runTransition = async (action: 'pick' | 'pack' | 'ship') => {
    if (!order) {
      return
    }
    try {
      await onAction(
        () => api.transition(order.id, action, `ui ${action}`),
        `Order moved to next state (${action})`,
      )
    } catch {
      return
    }
    await refresh()
  }

  const runCancel = async () => {
    if (!order) {
      return
    }
    try {
      await onAction(() => api.cancelOrder(order.id, 'cancelled from UI'), 'Order cancelled')
    } catch {
      return
    }
    await refresh()
  }

  return (
    <div className="grid">
      <div className="card">
        <h2>Submit order</h2>
        <form onSubmit={submitOrder}>
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
                <label htmlFor={`line-qty-${index}`}>Line quantity</label>
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
          <button type="button" onClick={() => void refresh()}>
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
                  onClick={() => void runTransition(nextAction(order.status)!)}
                >
                  {nextAction(order.status)}
                </button>
              )}
              {cancellable(order.status) && (
                <button
                  type="button"
                  className="danger"
                  disabled={busy}
                  onClick={() => void runCancel()}
                >
                  Cancel
                </button>
              )}
              {!nextAction(order.status) && !cancellable(order.status) && (
                <span className="muted">Terminal state</span>
              )}
            </div>

            <h3>Items</h3>
            <div className="table-wrap">
              <table>
                <caption>Order items and their reservation status.</caption>
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
                <caption>Append-only inventory movements for this order.</caption>
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
                  {(detail?.movements ?? []).length === 0 && (
                    <tr>
                      <td colSpan={5}>No movements for this order yet.</td>
                    </tr>
                  )}
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
