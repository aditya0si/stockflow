export interface BalanceRow {
  sku_id: string
  code: string
  name: string
  on_hand: number
  reserved: number
  available: number
  updated_at: string
}

export interface OrderItem {
  id: string
  sku_id: string
  sku: string
  quantity: number
  reservation_status: string
}

export type OrderStatus = 'accepted' | 'picking' | 'packed' | 'shipped' | 'cancelled'

export interface OrderDetail {
  order: Order
  events: FulfilmentEvent[]
  movements: Movement[]
}

export interface Order {
  id: string
  status: OrderStatus
  items: OrderItem[] | null
  created_at: string
  updated_at: string
  cancelled_at?: string
  shipped_at?: string
}

export interface Movement {
  id: number
  sku_id: string
  delta_on_hand: number
  delta_reserved: number
  kind: string
  actor: string
  reason?: string
  order_id?: string
  created_at: string
}

export interface FulfilmentEvent {
  id: number
  order_id: string
  from_status: string
  to_status: string
  actor: string
  reason?: string
  created_at: string
}

export interface Finding {
  id: string
  check_name: string
  sku_id?: string
  order_id?: string
  entity_ref?: string
  expected: string
  observed: string
  details?: Record<string, unknown>
}

export interface ReconciliationRun {
  id: string
  status: 'clean' | 'findings'
  checks_run: number
  findings_count: number
  started_at: string
  finished_at: string
  findings?: Finding[] | null
}

export interface Session {
  authenticated: boolean
  username?: string
  csrf_token?: string
  expires_at?: string
  demo_mode?: boolean
}

export interface Problem {
  type: string
  title: string
  status: number
  code?: string
  detail?: string
  extensions?: Record<string, unknown>
}
