import { useState, type FormEvent } from 'react'
import { api, errorMessage } from '../api'
import type { BalanceRow } from '../types'
import type { Notice } from './Notice'

export type RunAction = (action: () => Promise<unknown>, successText: string) => Promise<void>

interface StockPanelProps {
  balances: BalanceRow[]
  busy: boolean
  onRefresh: () => Promise<void>
  onNotice: (notice: Notice) => void
  onAction: RunAction
}

const emptyReceive = { sku: '', quantity: 1, reason: '' }
const emptySku = { code: '', name: '', opening_quantity: 0, opening_reason: '' }

export function StockPanel({ balances, busy, onRefresh, onNotice, onAction }: StockPanelProps) {
  const [receive, setReceive] = useState(emptyReceive)
  const [sku, setSku] = useState(emptySku)

  const refresh = async () => {
    try {
      await onRefresh()
    } catch (error) {
      onNotice({ kind: 'error', text: errorMessage(error) })
    }
  }

  const submitReceive = async (event: FormEvent) => {
    event.preventDefault()
    try {
      await onAction(() => api.receive(receive), `Received ${receive.quantity} of ${receive.sku}`)
    } catch {
      // Keep the form values so the operator can fix and resubmit.
      return
    }
    setReceive(emptyReceive)
    await refresh()
  }

  const submitSku = async (event: FormEvent) => {
    event.preventDefault()
    try {
      await onAction(() => api.createSku(sku), `Created ${sku.code}`)
    } catch {
      return
    }
    setSku(emptySku)
    await refresh()
  }

  return (
    <div className="grid">
      <div className="card">
        <div className="card-head">
          <h2>SKUs and balances</h2>
          <button type="button" onClick={() => void refresh()}>
            Refresh
          </button>
        </div>
        <div className="table-wrap">
          <table>
            <caption>SKU balances: on hand, reserved, and available units.</caption>
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
        <form onSubmit={submitReceive}>
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
        <form onSubmit={submitSku}>
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
