# Frontend dependency upgrade plan

This document records the exact `npm audit` result for the operator UI at freeze
time, explains what the findings do and do not affect, and defines the checks a
future upgrade must pass before the advisories can be closed.

It does not claim the advisories are harmless. It states scope and tracks a
concrete upgrade path.

## Audit snapshot

Captured `2026-09-26` from `web/` with the committed `package-lock.json`:

```sh
cd web && npm audit
```

Reported totals:

```text
5 vulnerabilities (3 moderate, 1 high, 1 critical)
```

Installed versions at freeze time: `vite@5.4.21`, `vitest@2.1.9`,
`vite-node@2.1.9`, `@vitest/mocker@2.1.9`, `esbuild@0.21.5`. All are
`devDependencies` in `web/package.json`.

| Package | Severity | Advisory | Scope |
| --- | --- | --- | --- |
| `vitest` | critical | [GHSA-5xrq-8626-4rwp](https://github.com/advisories/GHSA-5xrq-8626-4rwp) — arbitrary file read/execute while the Vitest **UI** server is listening (range `<3.2.6`) | Vitest UI dev server only |
| `vite` | high | [GHSA-fx2h-pf6j-xcff](https://github.com/advisories/GHSA-fx2h-pf6j-xcff) — `server.fs.deny` bypass via Windows alternate paths (range `<=6.4.2`) | Running Vite dev server on Windows |
| `vite` | moderate | [GHSA-4w7w-66w2-5vf9](https://github.com/advisories/GHSA-4w7w-66w2-5vf9) — path traversal in optimized-deps `.map` handling | Vite dev server |
| `vite` | moderate | [GHSA-v6wh-96g9-6wx3](https://github.com/advisories/GHSA-v6wh-96g9-6wx3) — `launch-editor` NTLMv2 hash disclosure via UNC path handling on Windows | Local editor launch from the Vite dev server |
| `@vitest/mocker` | moderate | [GHSA-82fw-gwwq-j7x9](https://github.com/advisories/GHSA-82fw-gwwq-j7x9) — path traversal / arbitrary file read via redirect mock | Vitest test process |
| `esbuild` | moderate | [GHSA-67mh-4wv8-2f99](https://github.com/advisories/GHSA-67mh-4wv8-2f99) — any website can send requests to and read responses from the esbuild **dev server** | esbuild dev server / Vite dev transform |

`npm audit` collapses the three `vite` advisories into a single package entry and
reports that package at its highest severity (high); `vite-node` is reported
moderately through its `vite` dependency. The counts above are exactly what
`npm audit` printed.

## Why the shipped artifact is not the dev server

- The image build (`Dockerfile`, `web` stage) runs `npm ci` and `npm run build`,
  then copies only the generated static assets from `internal/webui/dist` into
  the Go binary. The final stage is `gcr.io/distroless/static-debian12:nonroot`
  and contains the Go binary only.
- There is no Node runtime, no `vite`, no `vitest`, and no dev server in the
  shipped image or in the running service. Playwright and axe exercise the
  production build served by that binary, not a dev server.
- Every advisory above requires a **running dev server** (Vite/esbuild) or the
  **Vitest UI process** to be exploitable. Those exist only in local development
  and CI build/test steps, which run trusted, repository-controlled code.

The residual exposure is therefore the local/CI toolchain, not the deployed
artifact. A future reader should still treat the advisories as real for those
environments.

## Upgrade tracking plan

1. Open a dedicated upgrade branch; do not bundle the dependency bump with
   product changes.
2. `npm audit fix --force` currently proposes `vitest@5` and `vite@8`, both
   breaking major bumps, so the upgrade is manual and reviewed, not automated.
3. Move `vite`, `vitest`, and their transitive packages together to a major
   line that carries the fixes (at the time of writing: `vitest >= 3.2.6` for
   the critical advisory and `vite > 6.4.2` for the `fs.deny` bypass; prefer the
   latest supported major tested with the pinned Node version).
4. Re-run the acceptance checks below and record the before/after `npm audit`
   output in the pull request.
5. If any advisory cannot be cleared without a breaking change to the build
   output, record the residual finding here with its scope and rationale; do not
   silently accept it.

## Upgrade acceptance checks

An upgrade is accepted only when **all** of the following pass on the upgraded
tree:

```sh
cd web
npm ci
npm run format:check
npm run typecheck
npm test                 # must remain 9/9 passing
npm run build            # must still emit into internal/webui/dist
npm audit                # must report 0 vulnerabilities, or document each residual
```

Plus, against the production stack:

```sh
docker compose up -d --build
curl -fsS localhost:8080/readyz   # 200
cd web && npx playwright test     # 6 passed, 1 gated screenshot test skipped
```

- The generated bundle must still be embedded and served by the Go binary; no
  new runtime dependency (Node, a dev server, or a proxy) may be introduced.
- CI must remain green on Node 22 (`actions/setup-node` pin in
  `.github/workflows/ci.yml`).
- The upgrade must not weaken the existing test suite or gates (no skipped
  tests beyond the screenshot gate, no relaxed assertions).

## Relation to release evidence

This plan is referenced from `README.md`, `SPEC.md`, and
`docs/release-evidence.md`. The audit output is reproducible from the committed
lockfile; re-run `cd web && npm audit` to confirm the state at any later commit.
