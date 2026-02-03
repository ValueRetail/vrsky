# VRSky Issue Execution Order

**Date**: January 27, 2026  
**Status**: ✅ All issues updated with dependencies

---

## 🎯 Recommended Execution Sequence

Follow this sequence to ensure dependencies are met and work can progress efficiently.

---

## Phase 1: Foundation (Week 1-4)

### Start Here - Critical Path

**Issue #1: Build Core Platform Foundation** ⭐ **START HERE**

- **Timeline**: Week 1-4 (Jan 27 - Feb 23)
- **Team**: 2-3 engineers
- **Dependencies**: None
- **Blocks**: Everything else
- **Status**: This is the critical path - foundation for entire platform

**Parallel Track:**

**Issue #14: Infrastructure, Deployment & Developer Tools**

- **Timeline**: Week 1-4 (Jan 27 - Feb 23)
- **Team**: 1 engineer (DevOps)
- **Dependencies**: None
- **Can Run Parallel**: With #1
- **Notes**: Infrastructure setup doesn't block feature development

---

## Phase 2: Core Services (Week 2-4)

These depend on #1 being substantially complete (Week 2+):

### Sub-Components of #1 (can be separate PRs)

**Issue #20: NATS KV State Tracking & Retry Logic**

- **Timeline**: Week 2-4 (Feb 3 - Feb 23)
- **Team**: 1-2 engineers
- **Dependencies**: #1 (Platform NATS KV operational)
- **Blocks**: #4, #8, #18, #19
- **Notes**: Basic version in #1 Week 2, this issue refines it

**Issue #21: Service Discovery for Tenant NATS Instances**

- **Timeline**: Week 3-4 (Feb 10 - Feb 23)
- **Team**: 1 engineer
- **Dependencies**: #1 (Tenant NATS provisioning)
- **Blocks**: #4, #19
- **Notes**: Basic version in #1 Week 3, this issue hardens the API

---

## Phase 3: Multi-Tenancy & Auth (Week 2-4)

**Issue #4: Multi-Tenant Isolation & Authentication**

- **Timeline**: Week 2-4 (Feb 3 - Feb 23)
- **Team**: 1-2 engineers
- **Dependencies**: #1, #20, #21
- **Blocks**: #18, #19
- **Notes**: Needs NATS instances and service discovery working

---

## Phase 4: Platform Enhancements (Week 2-6)

Can start once #1 and #4 have basic functionality:

**Issue #18: Build Connector SDK & Essential Connectors**

- **Timeline**: Week 2-6 (Feb 3 - Mar 9)
- **Team**: 2 engineers
- **Dependencies**: #1 (Component interfaces), #4 (Auth)
- **Blocks**: None
- **Notes**: Can start SDK once interfaces defined in #1

**Issue #8: API Gateway, Monitoring & Observability**

- **Timeline**: Week 3-6 (Feb 10 - Mar 9)
- **Team**: 1-2 engineers
- **Dependencies**: #1, #4
- **Blocks**: #22
- **Notes**: Needs core platform and auth to monitor/protect

---

## Phase 5: Advanced Features (Week 5-7)

These require multi-tenancy and observability to be operational:

**Issue #19: NATS Instance Auto-Scaling & Lifecycle Management**

- **Timeline**: Week 5-7 (Feb 24 - Mar 16)
- **Team**: 1-2 engineers
- **Dependencies**: #4 (Tenant provisioning), #21 (Service Discovery)
- **Blocks**: None
- **Notes**: Enhances platform but not critical for POC

**Issue #22: NATS Monitoring & Observability Dashboards**

- **Timeline**: Week 6-7 (Mar 3 - Mar 16)
- **Team**: 1 engineer
- **Dependencies**: #8 (Observability stack), #19 (NATS to monitor)
- **Blocks**: None
- **Notes**: Final polish on monitoring

---

## Phase 6: Testing & Validation (Week 9-11)

**Issue #15: Integration Testing, Performance & Demo Scenarios** 🏁 **FINISH LINE**

- **Timeline**: Week 9-11 (Mar 24 - Apr 15)
- **Team**: 2-3 engineers
- **Dependencies**: ALL ISSUES
- **Blocks**: None
- **Notes**: Final validation before POC delivery

---

## Dependency Graph

```
START
  │
  ├─→ #1 (Core Platform) ←── CRITICAL PATH
  │    │
  │    ├─→ #20 (State Tracking) ─┐
  │    │                          │
  │    ├─→ #21 (Service Discovery)┤
  │    │                          │
  │    └─→ #4 (Multi-Tenant) ←────┘
  │         │
  │         ├─→ #18 (Connectors)
  │         │
  │         ├─→ #8 (API Gateway & Observability)
  │         │    │
  │         │    └─→ #22 (NATS Monitoring)
  │         │
  │         └─→ #19 (Auto-Scaling)
  │
  └─→ #14 (Infrastructure) ←── PARALLEL TRACK
       │
       └─→ Enables deployment (not blocking)

ALL ISSUES
  │
  └─→ #15 (Testing & Demo) ←── FINAL VALIDATION
```

---

## Weekly Sprint Plan

### Week 1-2 (Jan 27 - Feb 9)

**Focus**: Foundation & Infrastructure

- #1: Platform NATS, Tenant NATS, basic provisioning, HTTP consumer/producer
- #14: Docker Compose, K8s cluster, Helm structure

### Week 2-4 (Feb 3 - Feb 23)

**Focus**: Core Platform Complete

- #1: Orchestrator, retry logic, state machine (finish)
- #20: NATS KV state tracking (refine)
- #21: Service discovery API (harden)
- #4: Multi-tenant isolation, auth, NATS provisioning
- #14: Helm charts, CI/CD (finish)

### Week 3-6 (Feb 10 - Mar 9)

**Focus**: Platform Enhancements

- #18: Connector SDK, HTTP/File/DB connectors
- #8: API Gateway, Prometheus, Grafana, Loki

### Week 5-7 (Feb 24 - Mar 16)

**Focus**: Advanced Features & Monitoring

- #19: Auto-scaling logic
- #22: NATS-specific dashboards and alerts

### Week 9-11 (Mar 24 - Apr 15)

**Focus**: Testing, Performance, Demo

- #15: Integration tests, load tests, 4 demo integrations, documentation

---

## Parallel Work Opportunities

### Week 1-4: Maximum Parallelization

- **Track 1**: #1 (Core Platform) - 2-3 engineers
- **Track 2**: #14 (Infrastructure) - 1 engineer

### Week 2-4: Add More Tracks

- **Track 1**: #1 (Finish orchestrator)
- **Track 2**: #20/#21 (State & Discovery) - Can be same engineer
- **Track 3**: #4 (Multi-tenant) - 1-2 engineers
- **Track 4**: #14 (Finish infra)

### Week 3-6: Feature Teams

- **Team 1**: #18 (Connectors) - 2 engineers
- **Team 2**: #8 (API Gateway & Observability) - 1-2 engineers
- **Team 3**: #4 (Multi-tenant finish if needed)

---

## Critical Milestones

### ✅ Milestone 1: Foundation Complete (Week 4 - Feb 23)

- Platform NATS operational
- Tenant NATS provisioning working
- First end-to-end integration running
- Multi-tenant isolation validated

### ✅ Milestone 2: Platform Feature Complete (Week 7 - Mar 16)

- All connectors implemented
- API Gateway operational
- Monitoring dashboards live
- Auto-scaling working

### ✅ Milestone 3: POC Delivery (Week 11 - Apr 15)

- All tests passing
- Performance validated (1,000+ msgs/sec)
- 4 demo integrations working
- Documentation complete

---

## How to Use This Guide

1. **Start with #1 and #14 immediately** (parallel tracks)
2. **Week 2**: Add #20, #21 work (can be PRs within #1)
3. **Week 2-3**: Start #4 once #1 basics working
4. **Week 3**: Add #18 (Connectors) and #8 (Gateway/Observability)
5. **Week 5+**: Add #19 (Auto-scaling) and #22 (Monitoring)
6. **Week 9+**: Final testing and demo (#15)

**Rule of Thumb**:

- Don't start an issue until its dependencies are at least 70% complete
- Use issue comments to coordinate handoffs between teams
- Update issue status weekly in standup

---

## Issue Status at a Glance

| Issue | Priority | Timeline  | Dependencies | Status               |
| ----- | -------- | --------- | ------------ | -------------------- |
| #1    | P0       | Week 1-4  | None         | ⭐ **START HERE**    |
| #14   | P3       | Week 1-4  | None         | **Parallel with #1** |
| #20   | P1       | Week 2-4  | #1           | After #1 Week 2      |
| #21   | P1       | Week 3-4  | #1           | After #1 Week 3      |
| #4    | P0       | Week 2-4  | #1, #20, #21 | After #1 basics      |
| #18   | P1       | Week 2-6  | #1, #4       | After #1, #4 basics  |
| #8    | P1       | Week 3-6  | #1, #4       | After #1, #4         |
| #19   | P1       | Week 5-7  | #4, #21      | After #4, #21        |
| #22   | P2       | Week 6-7  | #8, #19      | After #8, #19        |
| #15   | P3       | Week 9-11 | ALL          | 🏁 **FINAL**         |

---

**POC Deadline**: April 15, 2026 ✅

**Total Active Issues**: 10  
**Estimated Team**: 3-4 engineers (with parallel work)  
**Timeline**: 11 weeks

---

## Related Documentation

- [NATS_ARCHITECTURE.md](./NATS_ARCHITECTURE.md) - Hybrid NATS architecture
- [NATS_IMPLEMENTATION_ISSUES.md](./NATS_IMPLEMENTATION_ISSUES.md) - Detailed issue specs
- [ACCELERATED_TIMELINE.md](./ACCELERATED_TIMELINE.md) - POC timeline
- [PROJECT_INCEPTION.md](./PROJECT_INCEPTION.md) - Vision and requirements

---

**Last Updated**: January 27, 2026  
**Status**: ✅ All issues updated with execution order
