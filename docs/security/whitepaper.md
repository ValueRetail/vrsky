# VRSky security whitepaper

This is a concise overview of how VRSky protects customer data. It summarizes the
controls; the [security reference](../SECURITY.md) has the operational detail.

## Summary

VRSky is a multi-tenant integration platform. Security rests on five pillars:
encrypted secrets, encryption at rest, an immutable audit trail, mutual-TLS for
high-security connectors, and enforced tenant isolation. Each is implemented in
code and, where possible, gated in CI.

## 1. Secrets management

Connection credentials (passwords, API keys, tokens, connection strings, private
keys) are **never stored in plaintext**. When a pipeline is deployed, each
plaintext credential is encrypted with **AES-256-GCM** and stored as a tenant
secret; the connection config keeps only a `<field>_secret_id` reference.
Workers resolve the reference to plaintext at runtime via the secrets store. The
management API never returns secret values.

## 2. Encryption at rest

The master key (`ENCRYPTION_KEY`, 256-bit) encrypts all secrets and `vrsky-cli`
backups. Operators store it in a secret manager and can rotate it by re-wrapping
without downtime. The [encryption-at-rest checklist](../operator/encryption-at-rest.md)
covers go-live verification (ciphertext-only storage, key backup, TLS to the DB).

## 3. Audit log

Privileged and security-relevant actions (secret create/rotate/delete, role
changes, connection lifecycle, plan changes) are recorded in an append-only,
per-tenant audit log, queryable and exportable (JSONL) by tenant admins.

## 4. Mutual TLS for high-security connectors

HTTP connectors support **worker-terminated mutual TLS**. A webhook consumer can
require an inbound client certificate that chains to a per-connection CA
(rejecting otherwise, including on the plain port); an HTTP producer can present
a client certificate to an mTLS-required endpoint. Cert material rides the same
encrypted-secret path. Rejections are observable
(`webhook_client_cert_failures_total`). See [HTTP & webhooks](../connectors/http.md).

## 5. Tenant isolation

Every resource is scoped to a tenant (workspace). API requests carry the active
tenant; data-store queries filter by `tenant_id`, and a custom CI linter
(`lint-tenant-filter`) **fails the build** if a tenant-scoped query is missing
its filter. Cross-tenant data sharing is explicit and consent-based: a request
is created, approved by the data owner, and only then bridges data, with every
access logged.

## Transport & gateway

The UI and API share an origin behind a gateway (Traefik) that applies
**per-tenant rate limiting** keyed to the subscription plan. Session auth uses
`SameSite` cookies; service-to-service calls (e.g. the OAuth token endpoint) use
a shared service token.

## Reporting

Security issues should be reported privately to the maintainers; please do not
open a public issue for a suspected vulnerability.
