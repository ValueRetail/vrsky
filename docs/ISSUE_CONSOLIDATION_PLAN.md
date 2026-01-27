# Issue Consolidation Plan - From 17 to 8 Action-Oriented Tasks

**Created**: January 27, 2026  
**Rationale**: Reduce research overhead, focus on building. Consolidate related tasks into actionable sprints.

---

## Current State: 17 Research Issues (Too Many!)

**Problem**: Too much emphasis on research, not enough on execution. Many issues overlap and can be done together.

**Solution**: Consolidate into 8 action-oriented issues that combine research with implementation.

---

## New Consolidated Issues (8 Total)

### ✅ KEEP & Modify: Issue #1 - Build Core Platform Foundation
**Rename from**: "Research: Technology Stack Evaluation"  
**New Title**: "Build Core Platform Foundation (NATS + Components)"  
**Timeline**: Week 1-4 (Jan 27 - Feb 23)  
**Team**: 2-3 engineers

**Consolidates**:
- #1: Tech Stack (Decision: Go - done, no more research)
- #2: NATS Architecture (Build it, don't just research)
- #3: Core Platform Architecture (Design while building)
- #5: Component Model (Build Consumer/Producer/Converter/Filter)
- #9: Orchestration Engine (Simple state machine)

**Deliverables**:
- Go project structure set up
- NATS + JetStream + MinIO running locally
- Reference-based messaging working (>256KB → object storage)
- Component interfaces defined (Go)
- HTTP Consumer & Producer working
- Simple orchestrator executing pipelines
- First end-to-end integration running

**Action Items**:
1. Update issue #1 with consolidated scope
2. Close issues #2, #3, #5, #9 (link to #1)

---

### ✅ KEEP & Modify: Issue #4 - Multi-Tenant Isolation & Security
**Rename from**: "Research: Multi-Tenancy Architecture & Data Isolation"  
**New Title**: "Multi-Tenant Isolation & Authentication"  
**Timeline**: Week 2-4 (Feb 3 - Feb 23)  
**Team**: 1-2 engineers

**Consolidates**:
- #4: Multi-Tenancy (NATS accounts, tenant isolation)
- #7: Security & Authentication (API keys, basic auth)

**Deliverables**:
- NATS account per tenant
- Tenant CRUD API
- API key authentication
- Tenant isolation validated
- Basic quota management

**Action Items**:
1. Update issue #4 with consolidated scope
2. Close issue #7 (link to #4)

---

### ✅ NEW ISSUE: Build Connector SDK & Essential Connectors
**New Issue** (create this)  
**Timeline**: Week 2-6 (Feb 3 - Mar 9)  
**Team**: 2 engineers

**Consolidates**:
- #6: SDK Design (Build minimal SDK, not comprehensive research)
- Part of #5: Component implementations

**Deliverables**:
- Connector SDK (Go interfaces + helpers)
- HTTP REST Consumer & Producer
- File Consumer & Producer
- PostgreSQL Consumer & Producer (CDC + writer)
- JSON/XML Converters
- Basic filters (field mapping, routing)
- SDK documentation & examples

**Action Items**:
1. Create new issue with this scope
2. Close issue #6 (link to new issue)

---

### ✅ KEEP & Modify: Issue #14 - Infrastructure & Developer Setup
**Rename from**: "Research: Deployment & Infrastructure Architecture"  
**New Title**: "Infrastructure, Deployment & Developer Tools"  
**Timeline**: Week 1-4 (Jan 27 - Feb 23)  
**Team**: 1 engineer (DevOps)

**Consolidates**:
- #14: Deployment & Infrastructure
- #16: Developer Experience & Tooling

**Deliverables**:
- Docker Compose for local development
- Kubernetes cluster (GKE/EKS/AKS)
- Helm charts for deployment
- GitHub Actions CI/CD pipeline
- Basic CLI tool (vrsky-cli)
- Development documentation

**Action Items**:
1. Update issue #14 with consolidated scope
2. Close issue #16 (link to #14)

---

### ✅ KEEP & Modify: Issue #8 - API Gateway & Observability
**Rename from**: "Research: API Gateway & Service Mesh Design"  
**New Title**: "API Gateway, Monitoring & Observability"  
**Timeline**: Week 3-6 (Feb 10 - Mar 9)  
**Team**: 1-2 engineers

**Consolidates**:
- #8: API Gateway (Kong or Traefik - quick decision, no service mesh)
- #13: Observability (Prometheus + Grafana + Loki)

**Deliverables**:
- Kong/Traefik API gateway configured
- REST API exposed through gateway
- Prometheus metrics collection
- Grafana dashboards (platform health, integrations, tenants)
- Loki for log aggregation
- Basic alerting

**Action Items**:
1. Update issue #8 with consolidated scope
2. Close issue #13 (link to #8)

---

### ✅ KEEP & Modify: Issue #15 - Testing & Demo Integrations
**Rename from**: "Research: Performance & Scalability Testing Strategy"  
**New Title**: "Integration Testing, Performance & Demo Scenarios"  
**Timeline**: Week 9-11 (Mar 24 - Apr 15)  
**Team**: 2-3 engineers

**Consolidates**:
- #15: Performance Testing (Load testing, benchmarks)
- #17: Documentation (Basic docs for POC)

**Deliverables**:
- Integration test suite (E2E tests)
- Load testing setup (k6)
- Performance validation (1,000+ msgs/sec)
- 4 demo integrations working:
  1. GitHub Webhook → Slack
  2. CSV File → PostgreSQL
  3. PostgreSQL CDC → HTTP API
  4. Multi-step workflow
- Getting started guide
- API reference documentation
- Demo videos/walkthroughs

**Action Items**:
1. Update issue #15 with consolidated scope
2. Close issue #17 (link to #15)

---

### ❌ CLOSE/DEFER: Issue #10 - Marketplace
**Status**: DEFER to post-POC  
**Rationale**: For POC, basic connector listing in UI is sufficient. Full marketplace (payments, reviews, publishing workflow) is post-POC.

**POC Scope** (build as part of UI):
- List available connectors
- Basic connector details page
- Install connector button

**Post-POC Features**:
- Marketplace publishing workflow
- Payment processing & revenue sharing
- Ratings & reviews
- Developer portal

**Action Items**:
1. Close issue #10 with comment explaining deferral
2. Basic connector listing built as part of UI (no separate issue needed)

---

### ❌ CLOSE/DEFER: Issue #11 - Storage-as-a-Service
**Status**: DEFER to post-POC  
**Rationale**: Core platform is ephemeral. Long-term storage is a separate paid service, not needed for POC.

**POC Scope**:
- Temporary storage only (MinIO/S3 with TTL cleanup)
- No long-term archival
- No state persistence service

**Action Items**:
1. Close issue #11 with comment explaining deferral

---

### ❌ CLOSE/DEFER: Issue #12 - Cross-Tenant Integration
**Status**: DEFER to post-POC  
**Rationale**: POC focuses on tenant isolation. Cross-tenant collaboration is a future feature.

**POC Scope**:
- Strong tenant isolation (NATS accounts)
- No cross-tenant data sharing
- No partnership features

**Action Items**:
1. Close issue #12 with comment explaining deferral

---

## Summary of Changes

### Before (17 issues):
- P0: 4 issues
- P1: 5 issues
- P2: 3 issues
- P3: 5 issues
- **Total: 17 research-heavy issues**

### After (8 issues):
1. ✅ Build Core Platform Foundation (#1 - modified)
2. ✅ Multi-Tenant Isolation & Security (#4 - modified)
3. ✅ Build Connector SDK & Connectors (NEW)
4. ✅ Infrastructure & Developer Setup (#14 - modified)
5. ✅ API Gateway & Observability (#8 - modified)
6. ✅ Testing & Demo Integrations (#15 - modified)
7. ❌ Marketplace (DEFER - close #10)
8. ❌ Storage-as-a-Service (DEFER - close #11)
9. ❌ Cross-Tenant (DEFER - close #12)

**Total: 6 active build-focused issues + 3 deferred**

### Issues to Close:
- #2 → merged into #1
- #3 → merged into #1
- #5 → merged into #1 & new SDK issue
- #6 → merged into new SDK issue
- #7 → merged into #4
- #9 → merged into #1
- #10 → deferred to post-POC
- #11 → deferred to post-POC
- #12 → deferred to post-POC
- #13 → merged into #8
- #16 → merged into #14
- #17 → merged into #15

**Result: 17 → 6 active issues (65% reduction)**

---

## Benefits of Consolidation

1. **Less Overhead**: Fewer issues to track and update
2. **Action-Oriented**: Focus on building, not researching
3. **Clearer Ownership**: One engineer/team per consolidated issue
4. **Faster Execution**: Less context switching
5. **POC-Focused**: Defer non-essential features

---

## Next Steps

1. Review this consolidation plan
2. If approved, execute consolidation:
   - Update 6 issues with new scope
   - Close 11 issues with explanation
   - Create 1 new issue (SDK & Connectors)
3. Update project board with new structure
4. Communicate changes to team

---

**Approval Status**: Pending  
**Estimated Time to Execute**: 30 minutes  
**Ready to Execute**: Yes
