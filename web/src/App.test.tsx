import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App'

function jsonResponse(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => JSON.stringify(body),
  }
}

function installFetch(routes: Record<string, unknown>) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url in routes) {
      return Promise.resolve(jsonResponse(200, routes[url]))
    }
    return Promise.resolve(jsonResponse(404, { code: 'not_found', title: 'Not found' }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
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

describe('App', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders the stock table with balances', async () => {
    installFetch({
      '/inventory/balances': balances,
      '/orders': { orders: [] },
      '/reconciliation/runs': { runs: [] },
    })

    render(<App />)

    expect(screen.getByRole('heading', { name: /StockFlow operator/i })).toBeInTheDocument()
    expect(await screen.findByText('DEMO-TEE')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('moves between the stock, order, and reconciliation views with tabs', async () => {
    installFetch({
      '/inventory/balances': balances,
      '/orders': { orders: [] },
      '/reconciliation/runs': { runs: [] },
    })

    render(<App />)

    await screen.findByText('DEMO-TEE')

    fireEvent.click(screen.getByRole('tab', { name: 'Orders' }))
    expect(screen.getByRole('heading', { name: 'Submit order' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('tab', { name: 'Reconciliation' }))
    expect(screen.getByRole('button', { name: 'Run report' })).toBeInTheDocument()
  })

  it('shows reconciliation findings for a selected run', async () => {
    installFetch({
      '/inventory/balances': balances,
      '/orders': { orders: [] },
      '/reconciliation/runs': {
        runs: [
          {
            id: 'run-1',
            status: 'findings',
            checks_run: 5,
            findings_count: 1,
            started_at: '2026-01-01T00:00:00Z',
            finished_at: '2026-01-01T00:00:01Z',
          },
        ],
      },
      '/reconciliation/runs/run-1': {
        id: 'run-1',
        status: 'findings',
        checks_run: 5,
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
    })

    render(<App />)

    await screen.findByText('DEMO-TEE')
    fireEvent.click(screen.getByRole('tab', { name: 'Reconciliation' }))
    fireEvent.click(await screen.findByRole('button', { name: /findings/i }))

    await waitFor(() => {
      expect(screen.getByText('reserved_matches_active_reservations')).toBeInTheDocument()
    })
    expect(screen.getByText('DEMO-TEE')).toBeInTheDocument()
  })
})
