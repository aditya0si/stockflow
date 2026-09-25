import { useState, type FormEvent } from 'react'
import { api, errorMessage } from '../api'
import type { Session } from '../types'

interface LoginPanelProps {
  onAuthenticated: (session: Session) => void
}

export function LoginPanel({ onAuthenticated }: LoginPanelProps) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const session = await api.login(username, password)
      onAuthenticated(session)
    } catch (loginError) {
      // Values are intentionally preserved so the operator can correct them.
      setError(errorMessage(loginError))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login">
      <h1>StockFlow operator sign in</h1>
      <p className="subtitle">
        Single-operator inventory reservation and fulfilment. Credentials are configured by the
        server; this demo has one operator.
      </p>
      <form onSubmit={submit} noValidate>
        <label htmlFor="login-username">Username</label>
        <input
          id="login-username"
          name="username"
          autoComplete="username"
          required
          value={username}
          onChange={(event) => setUsername(event.target.value)}
        />

        <label htmlFor="login-password">Password</label>
        <input
          id="login-password"
          name="password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />

        {error && (
          <p className="notice error" role="alert">
            {error}
          </p>
        )}

        <button type="submit" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </div>
  )
}
