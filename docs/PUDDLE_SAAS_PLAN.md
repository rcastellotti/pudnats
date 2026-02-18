# Puddle SaaS Plan (pudnats.com/<ID>)

## 1) Product Direction
Convert Pudnats from single-board deployment software into a hosted multi-tenant service where each customer owns one or more boards called **puddles**.

Core identity model:
- URL: `https://pudnats.com/<puddle_id>`
- A puddle is a tenant boundary for users, entries, settings, and billing state.
- Creating a puddle is a paid operation (Stripe), and the creator becomes puddle admin.

## 2) Customer Story
- A user visits Pudnats (or uses CLI).
- They create a puddle (name + slug + plan + payment).
- After successful payment, puddle is provisioned and they are marked `admin` for that puddle.
- They invite users and operate the puddle board.

## 3) Minimal MVP Scope
1. Multi-tenant data model with strict puddle scoping.
2. Puddle creation flow via web UI and CLI.
3. Stripe Checkout for paid puddle creation.
4. Role-based permissions per puddle (`admin`, `member`).
5. Route isolation by puddle ID (`/p/<id>` or direct `/<id>`).

Out of MVP:
- Team-wide org hierarchies.
- Per-seat metering.
- Complex enterprise auth/SSO.

## 4) URL and Routing Model
Recommended:
- Canonical puddle path: `https://pudnats.com/<puddle_id>`
- API variant: `/api/puddles/<puddle_id>/...`

Alternative (cleaner long-term):
- Subdomain per puddle: `https://<puddle_id>.pudnats.com`

Tradeoff:
- Path-based is easier to ship fast.
- Subdomain-based is stronger for brand/tenant separation.

## 5) Data Model Changes
Add tenant primitives:

### New tables
- `puddles`
  - `id` (stable short ID/slug)
  - `display_name`
  - `owner_user_id`
  - `plan`
  - `billing_status`
  - `stripe_customer_id`
  - `stripe_subscription_id` (nullable depending on pricing model)
  - `created_at`, `updated_at`

- `puddle_memberships`
  - `puddle_id`
  - `user_id`
  - `role` (`admin`, `member`)
  - unique (`puddle_id`, `user_id`)

- `billing_events` (append-only audit)
  - `puddle_id`
  - `provider` (`stripe`)
  - `event_type`
  - `provider_event_id`
  - `payload_json`
  - `created_at`

### Existing tables: tenant scoping
- `entries`: add `puddle_id` (required)
- `action_logs`: add `puddle_id` (nullable for platform-level events)
- `users`: can remain global identity (recommended)

### Access rule
Every read/write query for puddle data must include `WHERE puddle_id = ?`.

## 6) Auth and RBAC Model
Current role model is global; SaaS should use puddle-scoped roles.

Proposed:
- Keep global user identity/token.
- Resolve membership per request:
  - authenticate token -> user
  - resolve target puddle from URL/path
  - enforce membership role from `puddle_memberships`

Permissions:
- `admin`: manage members/settings/billing-sensitive operations in puddle
- `member`: create/read entries within puddle

## 7) Paid Puddle Creation (Stripe)

### Web flow
1. User submits puddle slug/name.
2. API validates slug availability.
3. API creates Stripe Checkout Session (mode based on pricing model).
4. User completes payment.
5. Stripe webhook confirms payment.
6. API finalizes puddle creation and admin membership.

### CLI flow
1. `pudnats puddle create --id acme --name "Acme Eng" --plan starter`
2. CLI hits API; API returns checkout URL.
3. CLI prints URL and optionally polls for provisioning status.
4. Once paid, puddle becomes active and creator gets admin role.

### Safety constraints
- Do not provision active puddle before verified payment event.
- Enforce idempotency on webhook event IDs.
- Persist raw event payloads for audits/debugging.

## 8) API Endpoints to Add
- `POST /api/puddles` (auth): start paid puddle creation
- `GET /api/puddles/<id>`: puddle metadata for authorized members
- `POST /api/puddles/<id>/members` (admin)
- `GET /api/puddles/<id>/entries` (member+)
- `POST /api/puddles/<id>/entries` (member+)
- `POST /api/billing/stripe/webhook` (provider callback)

## 9) CLI UX Proposal
New commands:
- `pudnats puddle create --id <id> --name <name> --plan <plan>`
- `pudnats puddle status --id <id>`
- `pudnats puddle members add --id <id> --username <u> --role member`
- `pudnats puddle members list --id <id>`

Guidelines:
- CLI should use API for all tenant operations.
- Keep bootstrap-only local DB commands for emergency/recovery use.

## 10) Web UI Changes
- Landing page for creating/selecting puddles.
- Puddle-context view with clear badge (`Puddle: <id>`).
- Admin settings panel:
  - invite members
  - role changes
  - billing status

## 11) Monetization Options (Beyond Initial Paid Creation)

### A) One-time paid puddle creation
- Customer pays once to create puddle.
- Pros: simple, low billing complexity.
- Cons: weaker recurring revenue.

### B) Subscription per puddle (recommended baseline)
- Monthly/annual plan per puddle.
- Pros: predictable MRR.
- Cons: requires lifecycle handling (trial, dunning, cancellations).

### C) Base + usage pricing
- Base subscription + usage metric (entries/month, members, API calls).
- Pros: aligns price with value.
- Cons: requires metering and transparent billing UX.

### D) Seat-based pricing
- Price by active members in puddle.
- Pros: common B2B model.
- Cons: role and seat management complexity.

### E) Add-on marketplace
- paid add-ons: Slack digest, advanced exports, audit retention, SSO.
- Pros: expansion revenue.
- Cons: feature fragmentation risk.

## 12) Suggested Packaging
Starter:
- 1 puddle
- up to N members
- standard retention

Growth:
- more members
- advanced search/export
- integrations

Business:
- SSO, audit exports, higher limits, support SLA

## 13) Operational Requirements for SaaS
- Move from local SQLite to managed Postgres for multi-tenant scale and reliability.
- Add migration system and schema versioning.
- Add webhook signature validation.
- Add request tracing and centralized logs.
- Add abuse/rate-limit protections on public endpoints.

## 14) Security Checklist
- Tenant isolation tests for every data-access endpoint.
- Stripe webhook secret validation.
- No token or payment PII in logs.
- Principle of least privilege for admin operations.
- Support token revocation and rotation.

## 15) Rollout Plan (Phased)

Phase 1: Foundation
- Introduce puddles + memberships schema.
- Add puddle-scoped API paths.
- Preserve backward compatibility for single-tenant mode.

Phase 2: Billing gate
- Integrate Stripe Checkout + webhook handling.
- Gate puddle activation on payment confirmation.

Phase 3: Productization
- New web onboarding flow.
- CLI puddle commands.
- Admin membership management UI/API.

Phase 4: Monetization expansion
- Introduce subscription tiers / add-ons.
- Add metering and upgrade paths.

## 16) Success Metrics
- Puddle creation conversion rate.
- Paid conversion rate from create intent.
- D30 active puddles.
- Revenue per puddle (ARPPU).
- Churn by plan tier.

## 17) Recommended Next Implementation Tasks
1. Add puddles and memberships tables + migration path.
2. Refactor entry/actions APIs to require puddle context.
3. Add `POST /api/puddles` skeleton endpoint.
4. Add Stripe webhook endpoint with signature verification scaffolding.
5. Add CLI `puddle create` command returning checkout URL.
6. Add integration tests for tenant isolation and role checks.
