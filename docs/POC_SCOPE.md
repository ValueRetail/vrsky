# VRSky POC Scope - What's In and What's Out

**POC Release Date**: April 15, 2026  
**Status**: Defined  
**Purpose**: Single source of truth for POC scope boundaries

---

## Executive Summary

This document clearly defines what features and capabilities **MUST** be included in the POC (Proof of Concept) release by April 15, 2026, and what features are **deferred** to post-POC development.

**POC Goal**: Demonstrate a working, scalable integration platform with hybrid NATS architecture that can handle real-world integration scenarios at 1,000+ msgs/sec with multi-tenant isolation.

---

## ✅ IN SCOPE - POC Release (April 15, 2026)

### Core Platform Components

#### 1. Messaging Infrastructure

- ✅ **Platform NATS Cluster** (3-node HA with JetStream)
  - State tracking using NATS KV
  - Retry queue (max 3 attempts, exponential backoff)
  - Dead Letter Queue (7-day retention)
- ✅ **Tenant NATS Instances** (single-node, ephemeral per tenant)
  - Auto-provisioning based on thresholds (50 integrations OR 100K msgs/sec)
  - Lifecycle management (create, monitor, cleanup)
  - Physical isolation via Kubernetes NetworkPolicies
- ✅ **Reference-Based Messaging**
  - Messages >256KB stored in MinIO/S3
  - NATS carries lightweight references
  - Automatic TTL cleanup (15 minutes default)

#### 2. Multi-Tenancy

- ✅ Tenant CRUD operations via API
- ✅ API key authentication
- ✅ Physical isolation (dedicated NATS instances)
- ✅ Kubernetes NetworkPolicies for network isolation
- ✅ Basic quota tracking (integration count, message volume)
- ❌ Cross-tenant data sharing (POST-POC)
- ❌ SSO/SAML/OAuth (POST-POC)

#### 3. Integration Management

- ✅ Integration CRUD API (create, update, delete, start, stop)
- ✅ Integration status monitoring
- ✅ Simple workflow orchestration (consumer → converter → filter → producer)
- ✅ Error handling with retry logic
- ✅ Failed message handling (DLQ)
- ✅ Integration execution logs
- ❌ Advanced orchestration (loops, complex branching) (POST-POC)
- ❌ Scheduled integrations with cron syntax (BASIC version in POC, ADVANCED post-POC)

#### 4. Component Framework

- ✅ **Consumer Interface** (data ingestion from external systems)
- ✅ **Producer Interface** (data delivery to external systems)
- ✅ **Converter Interface** (data transformation)
- ✅ **Filter Interface** (routing and conditional logic)
- ✅ Component lifecycle management
- ✅ Component configuration via API

#### 5. Connectors (Minimum 6)

**Consumers** (3):

- ✅ HTTP REST Consumer (webhook receiver)
- ✅ File Consumer (directory watcher)
- ✅ PostgreSQL Consumer (Change Data Capture using logical replication)

**Producers** (3):

- ✅ HTTP REST Producer (API caller with retry)
- ✅ File Producer (write to filesystem)
- ✅ PostgreSQL Producer (bulk insert/update)

**Converters** (2):

- ✅ JSON Converter (transformation, field mapping)
- ✅ XML Converter (XML ↔ JSON)

**Filters** (2):

- ✅ Field Mapping Filter (rename, extract fields)
- ✅ Conditional Routing Filter (if-then-else logic)

**Out of Scope for POC**:

- ❌ Kafka Consumer/Producer (POST-POC)
- ❌ SFTP Consumer/Producer (POST-POC)
- ❌ Database connectors beyond PostgreSQL (POST-POC)
- ❌ Cloud service connectors (AWS S3, Azure, GCP) (POST-POC)

#### 6. Connector SDK

- ✅ Go interfaces for Consumer/Producer/Converter/Filter
- ✅ Helper utilities (logging, config, error handling)
- ✅ Documentation with examples
- ✅ Template project for new connectors
- ❌ Connector sandboxing (POST-POC)
- ❌ Connector versioning (POST-POC)

### Infrastructure & Operations

#### 7. API Gateway

- ✅ Kong or Traefik deployed
- ✅ REST API routing
- ✅ Basic rate limiting
- ❌ Advanced security (WAF, DDoS protection) (POST-POC)
- ❌ gRPC gateway (POST-POC)

#### 8. Observability

- ✅ **Metrics** (Prometheus)
  - Platform health metrics
  - Integration execution metrics
  - NATS metrics (both Platform and Tenant instances)
  - Message throughput and latency
- ✅ **Dashboards** (Grafana)
  - Platform overview dashboard
  - Tenant metrics dashboard
  - NATS health dashboard (Platform + Tenant NATS)
  - Integration monitoring dashboard
- ✅ **Logging** (Loki)
  - Structured logs from all services
  - Centralized log aggregation
  - Searchable logs by tenant, integration, component
- ✅ **Alerting** (Basic)
  - Critical service down alerts
  - NATS cluster health alerts
  - High error rate alerts
- ❌ Distributed tracing (Jaeger/Tempo) (POST-POC)
- ❌ Advanced anomaly detection (POST-POC)

#### 9. Deployment

- ✅ **Local Development** (Docker Compose)
  - NATS (Platform cluster + sample tenant instance)
  - PostgreSQL
  - MinIO
  - All platform services
- ✅ **Kubernetes Deployment** (Helm charts)
  - Platform NATS cluster (StatefulSet)
  - Tenant NATS instances (Deployment, auto-scaled)
  - All platform services
  - PostgreSQL (can be external)
  - MinIO or S3 integration
- ✅ **CI/CD Pipeline** (GitHub Actions)
  - Automated build
  - Automated testing
  - Automated deployment to staging
- ❌ Multi-region deployment (POST-POC)
- ❌ Disaster recovery (POST-POC)

#### 10. Database

- ✅ PostgreSQL 15+ for metadata
  - Tenant configuration
  - Integration definitions
  - Component registry
  - Message state (mirrored from NATS KV for queries)
- ✅ Database migrations (managed)
- ❌ Database sharding (POST-POC)
- ❌ Multi-database support (POST-POC)

### User Interface

#### 11. Web UI (Basic)

- ✅ Admin dashboard (React + TailwindCSS)
- ✅ Tenant management (create, view, configure)
- ✅ Integration creation wizard
- ✅ Integration monitoring view (status, logs, metrics)
- ✅ Connector marketplace UI (list available connectors, basic install)
- ❌ Advanced workflow designer (visual drag-and-drop) (POST-POC)
- ❌ Analytics and reporting dashboards (POST-POC)

### Documentation

#### 12. POC Documentation

- ✅ **Getting Started Guide** (< 10 minutes to first integration)
- ✅ **Architecture Overview** (with diagrams)
- ✅ **NATS Architecture Guide** (hybrid model explained)
- ✅ **API Reference** (auto-generated from OpenAPI)
- ✅ **Connector Development Tutorial** (step-by-step)
- ✅ **Deployment Guide** (local Docker Compose + Kubernetes)
- ✅ **Demo Walkthroughs** (4 integration scenarios with videos)
- ✅ **Known Limitations** (what's not in POC)
- ✅ **Roadmap** (post-POC features)
- ❌ Comprehensive user manual (POST-POC)
- ❌ Advanced troubleshooting guide (POST-POC)

### Testing & Quality

#### 13. Testing Scope

- ✅ **Unit Tests** (70%+ coverage target)
- ✅ **Integration Tests** (E2E for all connectors)
- ✅ **Multi-Tenant Isolation Tests** (validate no cross-tenant leakage)
- ✅ **Load Testing** (1,000+ msgs/sec validated)
- ✅ **Failure Scenario Tests** (network failures, NATS crashes, service restarts)
- ✅ **Basic Security Testing** (API auth, tenant isolation)
- ❌ Performance testing at scale (10K+ msgs/sec) (POST-POC)
- ❌ Comprehensive security penetration testing (POST-POC)
- ❌ Chaos engineering (POST-POC)

### Demo Scenarios

#### 14. Required Demo Integrations (4)

**Demo 1: Webhook to Slack** (Simple, Real-time)

- GitHub webhook → JSON Converter → Slack Producer
- **Use Case**: Real-time event notifications
- **Validates**: HTTP Consumer, JSON transformation, HTTP Producer

**Demo 2: CSV File to Database** (Batch Processing)

- CSV File Consumer → CSV-to-JSON Converter → PostgreSQL Producer
- **Use Case**: Automated data import from files
- **Validates**: File Consumer, data transformation, bulk database insert

**Demo 3: Database CDC to API** (Real-time Sync)

- PostgreSQL Consumer (CDC) → JSON Transformer → HTTP REST Producer
- **Use Case**: Real-time data synchronization to external API
- **Validates**: Change Data Capture, real-time streaming, API integration

**Demo 4: Multi-step Workflow** (Complex)

- HTTP Consumer → JSON Converter → Filter (conditional routing) → PostgreSQL Producer + Slack Producer (parallel)
- **Use Case**: Order processing with conditional routing and parallel execution
- **Validates**: Orchestration, filtering, parallel execution, error handling, monitoring

---

## ❌ OUT OF SCOPE - Post-POC (After April 15, 2026)

### Features Deferred to Q3-Q4 2026

#### 1. Marketplace Platform (Issue #10 - Deferred)

**POC Scope**: Basic connector listing in UI only

**Post-POC Features**:

- Connector publishing workflow (developer portal)
- Payment processing and revenue sharing
- Connector ratings and reviews
- Version management for connectors
- Automated connector approval process
- Connector usage analytics

#### 2. Storage-as-a-Service (Issue #11 - Deferred)

**POC Scope**: Temporary storage only (ephemeral platform, 15-min TTL)

**Post-POC Features**:

- Long-term message archival (compliance, audit trails)
- State persistence service (workflow checkpoints, batch processing state)
- Replay capability (re-process historical messages)
- User-provided storage backends (BYOS - Bring Your Own Storage)
- Retention policies and lifecycle management
- Search and query capabilities

#### 3. Cross-Tenant Integration & Permissions (Issue #12 - Deferred)

**POC Scope**: Strong single-tenant isolation only

**Post-POC Features**:

- Cross-tenant data sharing (opt-in)
- Partnership models (controlled collaboration)
- B2B integration scenarios
- Permission grants and revocations
- Audit trail for cross-tenant operations
- Tenant identity verification

#### 4. Advanced Security

**POC Scope**: API key authentication, basic isolation

**Post-POC Features**:

- SSO (Single Sign-On) integration
- SAML 2.0 support
- OAuth 2.0 provider
- Role-based access control (RBAC)
- Fine-grained permissions
- Encryption at rest
- Advanced audit logging

#### 5. Advanced Orchestration

**POC Scope**: Simple linear workflows (consumer → converter → filter → producer)

**Post-POC Features**:

- Complex branching logic
- Loops and iterations
- Sub-workflows
- Parallel execution with join/merge
- Human-in-the-loop approvals
- Workflow versioning

#### 6. Performance & Scale

**POC Scope**: 1,000+ msgs/sec validated

**Post-POC Features**:

- 10,000+ msgs/sec performance
- Auto-scaling based on load (horizontal scaling)
- Multi-region deployment
- Geo-replication
- Advanced caching
- Connection pooling optimizations

#### 7. Additional Connectors

**POC Scope**: 6 connectors (HTTP, File, PostgreSQL + converters/filters)

**Post-POC Connectors**:

- Apache Kafka Consumer/Producer
- RabbitMQ Consumer/Producer
- SFTP/FTP Consumer/Producer
- AWS S3 Consumer/Producer
- Azure Blob Storage Consumer/Producer
- Google Cloud Storage Consumer/Producer
- MySQL/MariaDB Consumer/Producer
- MongoDB Consumer/Producer
- Salesforce Consumer/Producer
- SAP Consumer/Producer
- Email (SMTP/IMAP) Consumer/Producer

#### 8. Operations & Reliability

**POC Scope**: Basic deployment and monitoring

**Post-POC Features**:

- Disaster recovery procedures
- Backup and restore capabilities
- Blue-green deployments
- Canary releases
- Circuit breakers
- Advanced health checks
- Self-healing mechanisms

---

## Success Criteria for POC

The POC is considered **successful** if by April 15, 2026, the following criteria are met:

### Functional Criteria

1. ✅ **Demo-able**: All 4 demo integration scenarios working live without errors
2. ✅ **Multi-tenant**: Multiple tenants (minimum 3) running integrations in isolation simultaneously
3. ✅ **Reference-based**: Large files (>1MB) flow through object storage correctly
4. ✅ **Hybrid NATS**: Platform NATS + Tenant NATS architecture fully operational
5. ✅ **Extensible**: Developer can create a new connector using SDK in < 2 hours
6. ✅ **6 Connectors**: All 6 POC connectors working correctly

### Performance Criteria

7. ✅ **Scalable**: Handles 1,000+ messages/second in load testing
8. ✅ **Low Latency**: P99 latency < 100ms for small messages (<10KB)
9. ✅ **Reliable**: 99%+ success rate for message delivery in testing
10. ✅ **Auto-Scaling**: Tenant NATS instances auto-provision when thresholds reached

### Operational Criteria

11. ✅ **Observable**: Metrics and logs available in Grafana/Loki dashboards
12. ✅ **Deployed**: Running on Kubernetes in staging environment
13. ✅ **Automated**: CI/CD pipeline builds, tests, and deploys automatically
14. ✅ **Monitored**: Alerting operational for critical failures

### User Experience Criteria

15. ✅ **Documented**: New user can create their first integration in < 30 minutes using documentation
16. ✅ **Usable**: Web UI functional for tenant and integration management
17. ✅ **Debuggable**: Clear error messages, logs, and debugging tools available

### Architecture Criteria

18. ✅ **Isolated**: Multi-tenant isolation validated (no data leakage between tenants)
19. ✅ **Resilient**: Platform survives single component failure (NATS node, worker, service)
20. ✅ **Ephemeral**: Messages only stored during transit, automatic cleanup working

---

## Scope Change Process

If during development a feature needs to be **added to POC** or **removed from POC**:

1. **Document the change** in this file (update ✅ IN SCOPE or ❌ OUT OF SCOPE)
2. **Update timeline** in `docs/ACCELERATED_TIMELINE.md` if schedule impact
3. **Update GitHub issue** with scope change explanation
4. **Notify team** via standup or Slack
5. **Assess POC release date impact** - if April 15 is at risk, escalate immediately

**Approval Required**: Any scope change must be approved by project lead.

---

## References

- **Architecture**: `docs/NATS_ARCHITECTURE.md`
- **Timeline**: `docs/ACCELERATED_TIMELINE.md`
- **Issue Execution Order**: `docs/ISSUE_EXECUTION_ORDER.md`
- **GitHub Issues**: https://github.com/ValueRetail/vrsky/issues

---

**Last Updated**: January 27, 2026  
**Status**: Locked for POC  
**Next Review**: March 1, 2026 (mid-development checkpoint)
