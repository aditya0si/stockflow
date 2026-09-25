import { useCallback, useEffect, useState } from 'react'
import { ApiError, api, errorMessage, setCSRFToken } from './api'
import { LoginPanel } from './components/LoginPanel'
import { NoticeBanner, type Notice } from './components/Notice'
import { OrdersPanel } from './components/OrdersPanel'
import { ReconciliationPanel } from './components/ReconciliationPanel'
import { StockPanel, type RunAction } from './components/StockPanel'
import { Tabs, type TabDefinition } from './components/Tabs'
import type { BalanceRow, Order, OrderDetail, ReconciliationRun, Session } from './types'

type Tab = 'stock' | 'orders' | 'reconciliation'
type AuthState = 'checking' | 'anonymous' | 'authenticated'

const tabs: Array<TabDefinition<Tab>> = [
  { value: 'stock', label: 'Stock' },
  { value: 'orders', label: 'Orders' },
  { value: 'reconciliation', label: 'Reconciliation' },
]

export default function App() {
  const [authState, setAuthState] = useState<AuthState>('checking')
  const [session, setSession] = useState<Session | null>(null)

  const [tab, setTab] = useState<Tab>('stock')
  const [notice, setNotice] = useState<Notice>(null)
  const [busy, setBusy] = useState(false)

  const [balances, setBalances] = useState<BalanceRow[]>([])
  const [orders, setOrders] = useState<Order[]>([])
  const [selectedOrderId, setSelectedOrderId] = useState<string | null>(null)
  const [orderDetail, setOrderDetail] = useState<OrderDetail | null>(null)
  const [runs, setRuns] = useState<ReconciliationRun[]>([])
  const [selectedRun, setSelectedRun] = useState<ReconciliationRun | null>(null)

  const expireSession = useCallback(() => {
    setCSRFToken('')
    setSession(null)
    setAuthState('anonymous')
    setBalances([])
    setOrders([])
    setOrderDetail(null)
    setSelectedOrderId(null)
    setRuns([])
    setSelectedRun(null)
    setNotice({ kind: 'error', text: 'Your session expired. Sign in again.' })
  }, [])

  // guard converts an authentication failure into a sign-in prompt and
  // re-throws every other error to the caller.
  const guard = useCallback(
    (error: unknown): never => {
      if (error instanceof ApiError && error.status === 401) {
        expireSession()
      }
      throw error
    },
    [expireSession],
  )

  const refreshBalances = useCallback(async () => {
    try {
      setBalances(await api.listBalances())
    } catch (error) {
      guard(error)
    }
  }, [guard])

  const refreshOrders = useCallback(async () => {
    try {
      setOrders(await api.listOrders())
    } catch (error) {
      guard(error)
    }
  }, [guard])

  const refreshRuns = useCallback(async () => {
    try {
      setRuns(await api.listReconciliationRuns())
    } catch (error) {
      guard(error)
    }
  }, [guard])

  const loadOrder = useCallback(
    async (id: string) => {
      try {
        const [order, events, movements] = await Promise.all([
          api.getOrder(id),
          api.orderEvents(id),
          api.movements({ order_id: id }),
        ])
        setOrderDetail({ order, events, movements })
      } catch (error) {
        guard(error)
      }
    },
    [guard],
  )

  const loadAll = useCallback(async () => {
    await Promise.all([refreshBalances(), refreshOrders(), refreshRuns()])
  }, [refreshBalances, refreshOrders, refreshRuns])

  const startSession = useCallback(
    async (next: Session) => {
      setCSRFToken(next.csrf_token ?? '')
      setSession(next)
      setAuthState('authenticated')
      setNotice(null)
      try {
        await loadAll()
      } catch {
        // guard already surfaced the failure (usually a session prompt).
      }
    },
    [loadAll],
  )

  useEffect(() => {
    let active = true
    void api
      .session()
      .then((current) => {
        if (!active) {
          return
        }
        if (current.authenticated) {
          void startSession(current)
        } else {
          setAuthState('anonymous')
        }
      })
      .catch(() => {
        if (active) {
          setAuthState('anonymous')
        }
      })
    return () => {
      active = false
    }
  }, [startSession])

  const runAction: RunAction = useCallback(
    async (action, successText) => {
      setBusy(true)
      setNotice(null)
      try {
        await action()
        setNotice({ kind: 'ok', text: successText })
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) {
          expireSession()
        } else {
          setNotice({ kind: 'error', text: errorMessage(error) })
        }
        throw error
      } finally {
        setBusy(false)
      }
    },
    [expireSession],
  )

  const handleLogout = async () => {
    try {
      await api.logout()
    } catch {
      // The local session is cleared regardless of the server response.
    }
    expireSession()
    setNotice(null)
  }

  const selectOrder = (id: string) => {
    setSelectedOrderId(id)
    void loadOrder(id).catch((error) => {
      if (error instanceof ApiError && error.status === 401) {
        return
      }
      setNotice({ kind: 'error', text: errorMessage(error) })
    })
  }

  const stockCodes = balances.map((balance) => balance.code)

  if (authState === 'checking') {
    return (
      <div className="app">
        <p role="status" aria-live="polite">
          Checking session…
        </p>
      </div>
    )
  }

  if (authState === 'anonymous' || !session) {
    return (
      <div className="app">
        {notice && <NoticeBanner notice={notice} />}
        <LoginPanel onAuthenticated={(next) => void startSession(next)} />
      </div>
    )
  }

  return (
    <div className="app">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="app-header">
        <div className="card-head">
          <div>
            <h1>StockFlow operator</h1>
            <p className="subtitle">
              Single-location inventory reservation and fulfilment. No offline writes.
            </p>
          </div>
          <div className="row">
            <span className="muted">
              Signed in as <strong>{session.username}</strong>
            </span>
            <button type="button" className="secondary" onClick={() => void handleLogout()}>
              Sign out
            </button>
          </div>
        </div>
        {session.demo_mode && (
          <p className="notice" role="status">
            Demo mode: the operator credential and session key are generated non-secret defaults. Do
            not expose this instance to an untrusted network.
          </p>
        )}
      </header>

      <nav aria-label="Sections">
        <Tabs tabs={tabs} active={tab} onChange={setTab} />
      </nav>

      <NoticeBanner notice={notice} />

      <main id="main">
        <section
          id="panel-stock"
          role="tabpanel"
          aria-labelledby="tab-stock"
          tabIndex={0}
          hidden={tab !== 'stock'}
        >
          <StockPanel
            balances={balances}
            busy={busy}
            onRefresh={refreshBalances}
            onNotice={setNotice}
            onAction={runAction}
          />
        </section>

        <section
          id="panel-orders"
          role="tabpanel"
          aria-labelledby="tab-orders"
          tabIndex={0}
          hidden={tab !== 'orders'}
        >
          <OrdersPanel
            orders={orders}
            stockCodes={stockCodes}
            busy={busy}
            selectedId={selectedOrderId}
            detail={orderDetail}
            onSelect={selectOrder}
            onRefresh={async () => {
              await refreshOrders()
              if (selectedOrderId) {
                await loadOrder(selectedOrderId)
              }
            }}
            onNotice={setNotice}
            onAction={runAction}
          />
        </section>

        <section
          id="panel-reconciliation"
          role="tabpanel"
          aria-labelledby="tab-reconciliation"
          tabIndex={0}
          hidden={tab !== 'reconciliation'}
        >
          <ReconciliationPanel
            runs={runs}
            busy={busy}
            selectedRun={selectedRun}
            onSelect={async (id) => {
              try {
                setSelectedRun(await api.getReconciliationRun(id))
              } catch (error) {
                if (error instanceof ApiError && error.status === 401) {
                  expireSession()
                  return
                }
                setNotice({ kind: 'error', text: errorMessage(error) })
              }
            }}
            onRun={async () => {
              try {
                await runAction(async () => {
                  const run = await api.runReconciliation()
                  setSelectedRun(run)
                  await refreshRuns()
                }, 'Reconciliation run completed (report only)')
              } catch {
                // runAction already surfaced the error.
              }
            }}
          />
        </section>
      </main>
    </div>
  )
}
