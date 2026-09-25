import type {
  BalanceRow,
  FulfilmentEvent,
  Movement,
  Order,
  Problem,
  ReconciliationRun,
} from './types'

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly problem?: Problem

  constructor(status: number, code: string, message: string, problem?: Problem) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.problem = problem
  }
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  headers?: Record<string, string>,
): Promise<T> {
  const finalHeaders: Record<string, string> = { ...(headers ?? {}) }
  if (body !== undefined) {
    finalHeaders['Content-Type'] = 'application/json'
  }

  const response = await fetch(path, {
    method,
    headers: finalHeaders,
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  const text = await response.text()
  let parsed: unknown = undefined
  if (text) {
    try {
      parsed = JSON.parse(text)
    } catch {
      parsed = undefined
    }
  }

  if (!response.ok) {
    const problem = parsed as Problem | undefined
    const message =
      problem?.detail ?? problem?.title ?? `request failed with status ${response.status}`
    throw new ApiError(response.status, problem?.code ?? 'unknown', message, problem)
  }
  return parsed as T
}

function query(params: Record<string, string | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value) {
      search.set(key, value)
    }
  }
  const encoded = search.toString()
  return encoded ? `?${encoded}` : ''
}

export const api = {
  listBalances: () =>
    request<{ balances: BalanceRow[] }>('GET', '/inventory/balances').then((r) => r.balances ?? []),

  createSku: (input: {
    code: string
    name: string
    actor: string
    opening_quantity: number
    opening_reason?: string
  }) => request<{ sku: { id: string; code: string }; balance: BalanceRow }>('POST', '/skus', input),

  receive: (input: { sku: string; quantity: number; actor: string; reason: string }) =>
    request<{ movement: Movement; balance: BalanceRow }>('POST', '/inventory/receipts', input),

  listOrders: () => request<{ orders: Order[] }>('GET', '/orders').then((r) => r.orders ?? []),

  getOrder: (id: string) => request<Order>('GET', `/orders/${id}`),

  createOrder: (scope: string, key: string, lines: Array<{ sku: string; quantity: number }>) =>
    request<Order>(
      'POST',
      '/orders',
      { lines },
      {
        'X-Caller-Scope': scope,
        'Idempotency-Key': key,
      },
    ),

  cancelOrder: (id: string, actor: string, reason: string) =>
    request<Order>('POST', `/orders/${id}/cancel`, { actor, reason }),

  transition: (id: string, action: 'pick' | 'pack' | 'ship', actor: string, reason: string) =>
    request<Order>('POST', `/orders/${id}/${action}`, { actor, reason }),

  orderEvents: (id: string) =>
    request<{ events: FulfilmentEvent[] }>('GET', `/orders/${id}/events`).then(
      (r) => r.events ?? [],
    ),

  movements: (params: { sku_id?: string; order_id?: string }) =>
    request<{ movements: Movement[] }>('GET', `/inventory/movements${query(params)}`).then(
      (r) => r.movements ?? [],
    ),

  runReconciliation: () => request<ReconciliationRun>('POST', '/reconciliation/runs', {}),

  listReconciliationRuns: () =>
    request<{ runs: ReconciliationRun[] }>('GET', '/reconciliation/runs').then((r) => r.runs ?? []),

  getReconciliationRun: (id: string) =>
    request<ReconciliationRun>('GET', `/reconciliation/runs/${id}`),
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.message} (${error.code})`
  }
  if (error instanceof Error) {
    return error.message
  }
  return 'unexpected error'
}

export function newIdempotencyKey(): string {
  return `ui-${crypto.randomUUID()}`
}
