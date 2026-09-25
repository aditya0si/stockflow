import type { ReconciliationRun } from '../types'

interface ReconciliationPanelProps {
  runs: ReconciliationRun[]
  busy: boolean
  selectedRun: ReconciliationRun | null
  onSelect: (id: string) => Promise<void>
  onRun: () => Promise<void>
}

export function ReconciliationPanel({
  runs,
  busy,
  selectedRun,
  onSelect,
  onRun,
}: ReconciliationPanelProps) {
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
                <caption>Reconciliation findings with expected and observed values.</caption>
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
