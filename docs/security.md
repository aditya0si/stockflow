# Security model and threat model

StockFlow is a single-operator inventory workbench for a local/demo deployment.
It is **not** enterprise authentication and does not claim to be. This document
states exactly what is protected, how, and what remains out of scope.

## Model

- **One operator.** There is a single username and password configured by
  environment (`STOCKFLOW_OPERATOR_USERNAME`,
  `STOCKFLOW_OPERATOR_PASSWORD_HASH`). There are no users, roles, tenants,
  invitations, password reset, MFA, or external identity providers.
- **Password storage.** The configured password is a bcrypt hash; plaintext is
  never committed. Generate a hash with
  `go run ./cmd/api hash-password '<password>'`.
- **Session.** Login issues an AES-256-GCM encrypted, authenticated cookie
  (`stockflow_session`). The cookie is `HttpOnly`, `SameSite=Strict`, `Path=/`,
  has a bounded expiry (`STOCKFLOW_SESSION_TTL`, default 12h), and is `Secure`
  outside demo mode. There is no server-side session store, so the cookie is the
  session authority.
- **CSRF.** Every state-changing request must carry `X-CSRF-Token` matching the
  token embedded in the session. The token is delivered by `POST /auth/login`
  and `GET /auth/session` and is held in memory by the UI. SameSite=Strict is a
  second layer.
- **Authorization.** Every API route except `GET /healthz`, `GET /readyz`,
  `POST /auth/login`, `GET /auth/session`, and `POST /auth/logout` requires a
  valid session. Reads are authenticated too; anonymous read-only demo access is
  not offered.
- **Audit identity.** Receipts, order creation, cancellations, pick/pack/ship,
  and reconciliation runs derive their actor from the authenticated session.
  `actor`, `X-Caller-Scope`, and other request-supplied identity fields are
  ignored and are covered by tests.
- **Login throttling.** Failed logins are counted per client address in a
  bounded in-memory window (`STOCKFLOW_LOGIN_MAX_ATTEMPTS`, default 10/minute).
  This is single-process by design: running multiple replicas would multiply the
  effective limit, and a shared store is out of scope.
- **Fail-closed startup.** Outside demo mode, a missing username, password
  hash, or session secret is a fatal configuration error. Demo defaults are only
  used when `STOCKFLOW_DEMO_MODE=true` is set explicitly, and the UI shows a
  banner while it is.

## Threat model

| Threat | Mitigation | Residual risk |
| --- | --- | --- |
| Unauthenticated mutation | Session required on all API routes; tested | A stolen/leaked session cookie works until expiry |
| Credential guessing | bcrypt hashing + bounded per-address throttle | Global/per-username throttle absent; distributed guessing untracked |
| CSRF | `X-CSRF-Token` bound to the session + SameSite=Strict | None known within the browser model |
| Forged actor/scope | Identity taken only from the session; tested | None known |
| Session tampering | AES-GCM authentication over the payload | Secret compromise forges sessions |
| Session expiry/replay | Bounded expiry; tampered/expired cookies rejected | Logout clears the cookie client-side; a copied token stays valid until expiry (no revocation list) |
| XSS reading the session | Cookie is HttpOnly, CSRF token in memory only | Any XSS can still act as the operator |
| Transport interception | `Secure` cookie outside demo mode | Deployer must terminate TLS correctly |
| Brute force / DoS | Login throttle | No general request rate limiting; a single process is a single target |
| Data exfiltration | No multi-user data model; one operator | Anyone with the credential sees all data |

## Out of scope

- Roles, permissions, multi-tenancy, teams, API tokens, OAuth/OIDC/SAML, MFA,
  password reset, audit-log shipping, secret rotation.
- Horizontal scaling, shared session/rate-limit stores, WAF, DDoS protection.
- Hosted/production hardening, TLS termination, backups of secrets, key
  management.

If StockFlow were ever exposed beyond a trusted environment, the operator
credential, session key, TLS, and a shared rate limiter would all need real
operational treatment first.
