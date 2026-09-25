import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, errorMessage } from './api'

function jsonResponse(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => JSON.stringify(body),
  }
}

describe('api client', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('lists balances from the inventory endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(200, {
        balances: [
          {
            sku_id: 'abc',
            code: 'DEMO-TEE',
            name: 'Demo T-Shirt',
            on_hand: 5,
            reserved: 2,
            available: 3,
            updated_at: '2026-01-01T00:00:00Z',
          },
        ],
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const balances = await api.listBalances()

    expect(balances).toHaveLength(1)
    expect(balances[0].available).toBe(3)
    expect(fetchMock).toHaveBeenCalledWith(
      '/inventory/balances',
      expect.objectContaining({ method: 'GET' }),
    )
  })

  it('sends caller scope and idempotency key when creating an order', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(201, { id: 'order-1', status: 'accepted' }))
    vi.stubGlobal('fetch', fetchMock)

    const order = await api.createOrder('operator', 'key-123', [{ sku: 'DEMO-TEE', quantity: 1 }])

    expect(order.id).toBe('order-1')
    const [, options] = fetchMock.mock.calls[0]
    expect(options.headers['X-Caller-Scope']).toBe('operator')
    expect(options.headers['Idempotency-Key']).toBe('key-123')
  })

  it('surfaces RFC 9457 problems as ApiError', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(409, {
          title: 'Illegal order transition',
          detail: 'cannot move order from shipped to cancelled',
          code: 'illegal_transition',
        }),
      ),
    )

    await expect(api.cancelOrder('order-1', 'operator', 'too late')).rejects.toMatchObject({
      name: 'ApiError',
      status: 409,
      code: 'illegal_transition',
    })
  })

  it('formats errors for the UI', () => {
    expect(errorMessage(new ApiError(409, 'illegal_transition', 'cannot move'))).toBe(
      'cannot move (illegal_transition)',
    )
    expect(errorMessage('plain string')).toBe('unexpected error')
  })
})
