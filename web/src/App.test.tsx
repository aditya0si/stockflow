import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { setCSRFToken } from './api'

function jsonResponse(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => JSON.stringify(body),
  }
}

type Route = { status: number; body: unknown }

function installFetch(routes: Record<string, Route | unknown>) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url in routes) {
      const route = routes[url]
      if (route && typeof route === 'object' && 'status' in route && 'body' in route) {
        const typed = route as Route
        return Promise.resolve(jsonResponse(typed.status, typed.body))
      }
      return Promise.resolve(jsonResponse(200, route))
    }
    return Promise.resolve(jsonResponse(404, { code: 'not_found', title: 'Not found' }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

const sessionRoute = {
  status: 200,
  body: { authenticated: true, username: 'operator', csrf_token: 'test-csrf' },
}

const balances = {
  balances: [
    {
      sku_id: 'sku-1',
      code: 'DEMO-TEE',
      name: 'Demo T-Shirt',
      on_hand: 5,
      reserved: 2,
      available: 3,
      updated_at: '2026-01-01T00:00:00Z',
    },
  ],
}

function baseRoutes(extra: Record<string, Route | unknown> = {}) {
  return {
    '/auth/session': sessionRoute,
    '/inventory/balances': balances,
    '/orders': { orders: [] },
    '/reconciliation/runs': { runs: [] },
    ...extra,
  }
}

describe('App', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    setCSRFToken('')
  })

  it('renders the stock table with balances once authenticated', async () => {
    installFetch(baseRoutes())

    render(<App />)

    expect(await screen.findByRole('heading', { name: /StockFlow operator/i })).toBeInTheDocument()
    expect(await screen.findByText('DEMO-TEE')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('moves between the stock, order, and reconciliation views with tabs', async () => {
    installFetch(baseRoutes())

    render(<App />)

    await screen.findByText('DEMO-TEE')

    fireEvent.click(screen.getByRole('tab', { name: 'Orders' }))
    expect(screen.getByRole('heading', { name: 'Submit order' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('tab', { name: 'Reconciliation' }))
    expect(screen.getByRole('button', { name: 'Run report' })).toBeInTheDocument()
  })

  it('supports arrow, Home, and End keyboard navigation on the tablist', async () => {
    installFetch(baseRoutes())

    render(<App />)
    await screen.findByText('DEMO-TEE')

    const stockTab = screen.getByRole('tab', { name: 'Stock' })
    stockTab.focus()
    fireEvent.keyDown(stockTab, { key: 'ArrowRight' })

    const ordersTab = screen.getByRole('tab', { name: 'Orders' })
    expect(ordersTab).toHaveAttribute('aria-selected', 'true')
    expect(ordersTab).toHaveFocus()

    fireEvent.keyDown(ordersTab, { key: 'End' })
    const reconciliationTab = screen.getByRole('tab', { name: 'Reconciliation' })
    expect(reconciliationTab).toHaveAttribute('aria-selected', 'true')
    expect(reconciliationTab).toHaveFocus()

    fireEvent.keyDown(reconciliationTab, { key: 'Home' })
    expect(screen.getByRole('tab', { name: 'Stock' })).toHaveAttribute('aria-selected', 'true')
    expect(stockTab).toHaveFocus()
  })

  it('shows reconciliation findings for a selected run', async () => {
    installFetch(
      baseRoutes({
        '/reconciliation/runs': {
          runs: [
            {
              id: 'run-1',
              status: 'findings',
              checks_run: 6,
              findings_count: 1,
              started_at: '2026-01-01T00:00:00Z',
              finished_at: '2026-01-01T00:00:01Z',
            },
          ],
        },
        '/reconciliation/runs/run-1': {
          id: 'run-1',
          status: 'findings',
          checks_run: 6,
          findings_count: 1,
          started_at: '2026-01-01T00:00:00Z',
          finished_at: '2026-01-01T00:00:01Z',
          findings: [
            {
              id: 'finding-1',
              check_name: 'reserved_matches_active_reservations',
              entity_ref: 'DEMO-TEE',
              expected: '1',
              observed: '0',
            },
          ],
        },
      }),
    )

    render(<App />)

    await screen.findByText('DEMO-TEE')
    fireEvent.click(screen.getByRole('tab', { name: 'Reconciliation' }))
    fireEvent.click(await screen.findByRole('button', { name: /findings/i }))

    await waitFor(() => {
      expect(screen.getByText('reserved_matches_active_reservations')).toBeInTheDocument()
    })
    // The reference also appears in the (hidden) stock table, so assert presence
    // rather than uniqueness.
    expect(screen.getAllByText('DEMO-TEE').length).toBeGreaterThan(0)
  })

  it('preserves form values and reports an actionable error when a mutation fails', async () => {
    const fetchMock = installFetch(
      baseRoutes({
        '/inventory/receipts': {
          status: 404,
          body: { title: 'Not found', detail: 'no sku with code NOPE', code: 'sku_not_found' },
        },
      }),
    )

    render(<App />)
    await screen.findByText('DEMO-TEE')

    fireEvent.change(screen.getByLabelText('SKU code'), { target: { value: 'NOPE' } })
    fireEvent.change(screen.getByLabelText('Quantity'), { target: { value: '4' } })
    fireEvent.change(screen.getByLabelText('Reason'), { target: { value: 'supplier delivery' } })
    fireEvent.click(screen.getByRole('button', { name: 'Receive' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('no sku with code NOPE')
    expect(alert).toHaveTextContent('sku_not_found')

    // Values are preserved so the operator can correct the input.
    expect(screen.getByLabelText('SKU code')).toHaveValue('NOPE')
    expect(screen.getByLabelText('Quantity')).toHaveValue(4)
    expect(screen.getByLabelText('Reason')).toHaveValue('supplier delivery')

    // No success notice and no data refresh were shown as if it completed.
    expect(screen.queryByText(/Received 4 of NOPE/)).not.toBeInTheDocument()
    const balanceCalls = fetchMock.mock.calls.filter(
      ([input]) => (typeof input === 'string' ? input : input.toString()) === '/inventory/balances',
    )
    expect(balanceCalls).toHaveLength(1)
  })
})
