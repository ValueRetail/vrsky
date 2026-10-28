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
covers go-live verification (ciphertext-only storage, key backup, TLS to the DB
where the deployment supports it).

On the hosted service, the database, message store and object storage keep
their data on Azure managed disks, which Azure encrypts at rest with
platform-managed keys. Traffic to the database stays on the cluster's private
network and isn't TLS-encrypted (see [Transport & gateway](#transport-gateway)).

## 3. Audit log

Privileged and security-relevant actions (secret create/rotate/delete, role
changes, connection lifecycle, plan changes) are recorded in an append-only,
per-tenant audit log, queryable and exportable (JSONL) by tenant admins.

## 4. Mutual TLS for high-security connectors

HTTP connectors support mutual TLS. Certificate material rides the same
encrypted-secret path as other credentials.

- **Outbound.** An HTTP producer can present a client certificate to an
  endpoint that requires one. This works on the hosted service and on
  self-hosted deployments.
- **Inbound.** A webhook consumer can require a client certificate that chains
  to a per-connection CA. The check runs in the worker, on its own TLS port,
  so that port has to be reachable from outside. The Docker Compose stack
  opens it; the Kubernetes manifests don't yet. The hosted service doesn't
  expose it either: TLS for webhooks ends at the ingress. A webhook connection set to require a client certificate there
  refuses every request rather than accepting one without a certificate. For
  inbound webhooks on the hosted service, use HMAC request signing.

Rejections are observable (`webhook_client_cert_failures_total`). See
[HTTP & webhooks](../connectors/http.md).

## 5. Tenant isolation

Every resource is scoped to a tenant (workspace). API requests carry the active
tenant; data-store queries filter by `tenant_id`, and a custom CI linter
(`lint-tenant-filter`) **fails the build** if a tenant-scoped query is missing
its filter. Cross-tenant data sharing is explicit and consent-based: a request
is created, approved by the data owner, and only then bridges data, with every
access logged.

## Transport & gateway

The UI and API share an origin. In production, TLS terminates at the
ingress (ingress-nginx, with a Let's Encrypt certificate from cert-manager);
inside the cluster, services talk over the cluster's private network without
TLS. A gateway (Traefik) that applies **per-tenant rate limiting** keyed to
the subscription plan exists for the Docker Compose stack but is not part of
the production deployment. Production relies on the limits built into the
services: plan quotas, failed-attempt limits on login, sign-up and
remote-agent registration, and a per-connection rate limit on data
ingestion. Session auth uses
`SameSite` cookies; service-to-service calls (e.g. the OAuth token endpoint) use
a shared service token.

## Reporting

Security issues should be reported privately to the maintainers; please do not
open a public issue for a suspected vulnerability.
