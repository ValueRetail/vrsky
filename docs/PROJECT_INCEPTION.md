# VRSky Project Inception

**Date**: January 27, 2026  
**Status**: Research Phase Kickoff  
**Purpose**: Document the original vision and requirements that shaped the VRSky integration platform

---

## Original Vision Statement

> "I want to build a highly scalable and fast integration platform that is intended to be some sort of a hub between systems, either internal systems or external systems."

## Core Requirements

### Platform Concept

The VRSky platform is designed around several key architectural concepts:

#### Integration Components

- **Consumers**: Receive data from external systems
- **Producers**: Send data to external systems
- **Converters**: Transform data between formats
- **Filters**: Route, filter, and process messages

These components should be **composable and scalable**, allowing users to build complex integration workflows.

#### Marketplace Model

A central marketplace/app store where:

- Users can **publish their own integrations** (free or paid)
- Platform owner takes a **small revenue share**
- Users can discover and enable connectors built by others
- Both internal connectors and external third-party connectors are available

#### Multi-Tenancy with Collaboration

- **Multi-tenant architecture** with strong isolation between users/companies
- **Cross-tenant interaction** - companies can collaborate and share data within the platform
- Users can **enable external connectors** so other users/companies can access their integrations
- Balance between isolation and controlled collaboration

### Architectural Principles

#### 1. Ephemeral Platform - No Persistent Storage

**Key Principle**: The platform should NOT be responsible for long-term data storage.

> "I don't think this platform should be responsible for storage, that might be something users/companies can buy as a service. Integrations sent through this platform should only be stored as long as it needs to be able to pass from origin to destination."

**Implications**:

- Messages are stored **only during transit** (temporary storage)
- Long-term storage is an **optional paid service** (Storage-as-a-Service)
- Platform focuses on **data flow**, not data persistence

#### 2. Reference-Based Messaging for Large Payloads

**Key Principle**: For large messages, use object storage with references instead of passing data through NATS.

> "Messages can be large, and for larger payloads, we should use some kind of temporary storage instead of sending the data through NATS. NATS should just then be used for signaling with reference to the stored data."

**Implementation Approach**:

- Small messages (<threshold): Sent directly through NATS
- Large messages (>threshold):
  - Payload stored in object storage (S3/GCS/Azure Blob)
  - NATS carries a lightweight reference (pre-signed URL)
  - Automatic TTL cleanup after delivery
- Threshold values to be determined through research

#### 3. Storage-as-a-Service (Optional)

Optional paid service providing:

- **Message Archive**: Long-term storage for compliance, audit trails, replay capability
- **State Persistence**: Checkpoints, workflow state, batch processing state
- **Not part of core platform** - this is an add-on service

## Technology Stack Decisions

### Backend Language: .NET 10 vs Go

**Original Question**:

> "I am wondering about the tech stack for the backend, and stands between 2 technologies/languages, .NET 10 or Go. What do you think would be the best suited for a massive scalable system like this?"

**Recommendation**: **Go**

**Rationale**:

- **Concurrency**: Go's goroutines and channels are perfect for handling thousands of concurrent integrations
- **Performance**: Lower memory footprint and faster startup times
- **Deployment**: Single binary simplifies containerization
- **Ecosystem**: Strong support for NATS, gRPC, cloud-native tooling
- **Operational simplicity**: Easier to debug and monitor in production

**Alternative Considered**: .NET 10

- Pros: Rich enterprise integration libraries, strong typing, better for .NET-heavy organizations
- Cons: Higher resource usage, more complex deployment, heavier runtime

### Message Transport: NATS

**Original Question**:

> "What tech should we use for transport of messages/data? Will NATS be sufficient, or would you recommend something else?"

**Recommendation**: **NATS with JetStream**

**Rationale**:

- **Performance**: 11M+ msgs/sec throughput, sub-millisecond latency
- **Multi-tenancy**: Good support for isolated accounts
- **JetStream**: Provides persistence, exactly-once delivery, replay capability
- **Lightweight**: Small footprint, fast startup, easy to operate
- **Cloud-native**: Perfect for Kubernetes deployments

**Alternatives Considered**:

- **Apache Kafka**: Better for very long retention, complex stream processing (heavier, more complex)
- **RabbitMQ**: Complex routing, multiple protocols (lower throughput, more resources)
- **Apache Pulsar**: Geo-replication, strong multi-tenancy (more complex, smaller ecosystem)

**Decision**: Start with NATS, add Kafka later if specific use cases require it.

## Research Tasks Defined

Based on the requirements above, 17 comprehensive research tasks were created and organized by priority:

### P0 - Critical Foundation (4 tasks)

1. Technology Stack Evaluation (.NET vs Go)
2. Message Transport Architecture (NATS Design)
3. Core Integration Platform Architecture
4. Multi-Tenancy Architecture & Data Isolation

### P1 - High Priority Core Platform (5 tasks)

5. Integration Component Model (Consumers, Producers, Converters, Filters)
6. Plugin/Connector SDK Design
7. Security & Authentication Architecture
8. API Gateway & Service Mesh Design
9. Data Flow & Orchestration Engine

### P2 - Medium Priority Business Layer (3 tasks)

10. Marketplace Platform Design
11. Storage-as-a-Service Design (Archive & State)
12. Cross-Tenant Integration & Permissions

### P3 - Lower Priority Operations (5 tasks)

13. Observability & Monitoring Strategy
14. Deployment & Infrastructure Architecture
15. Performance & Scalability Testing Strategy
16. Developer Experience & Tooling
17. Documentation & Onboarding Strategy

**Full details**: See [docs/tasks/README.md](tasks/README.md)

## Key Questions to Answer During Research

### Message Size and TTL Thresholds

> "We should decide on that in our research"

**Questions**:

- What size threshold should trigger reference-based messaging? (64KB? 256KB? 512KB?)
- How long should temporary data be retained during transit? (30s? 5min? 15min?)
- Should these be configurable per integration?

**Research Required**:

- Benchmark NATS performance at various payload sizes
- Analyze latency impact of inline vs reference-based messaging
- Cost analysis: NATS bandwidth vs object storage operations

### Storage Service Scope

**Confirmed Requirements**:

- Both message archive AND state persistence
- Object storage backend (S3/GCS recommended)
- Optional paid add-on, not core platform

**Questions to Research**:

- Default retention policies?
- Pricing model (per-GB, per-request, tiered)?
- User-provided storage backends (BYOS)?

### Cross-Tenant Collaboration

**Requirements**:

- Strong isolation by default
- Controlled data sharing between specific tenants
- Audit trail for all cross-tenant operations

**Questions to Research**:

- Permission model (grant-based vs request-based)?
- How to verify tenant identity?
- What happens when permissions are revoked mid-integration?

## Success Criteria

The VRSky platform will be considered successful when it achieves:

1. **Scalability**: Handle millions of messages per day with sub-100ms p99 latency
2. **Developer Experience**: New developers can build their first integration in <10 minutes
3. **Marketplace Adoption**: Thriving ecosystem of third-party connectors
4. **Multi-Tenancy**: Support 1000+ tenants with strong isolation
5. **Reliability**: 99.9%+ uptime with graceful degradation
6. **Performance**: Linear horizontal scaling (add nodes = add capacity)

## Timeline

**Research Phase**: 4-6 months (Q2 2026)

- P0 tasks: 7-9 weeks (sequential, critical path)
- P1 tasks: Can run partially in parallel
- P2-P4 tasks: Can be fully parallelized

**Estimated Team**: 3-4 engineers working in parallel

## Deliverables from Research Phase

Each research task should produce:

- **Architecture Decision Record (ADR)**: Document major decisions with rationale
- **Design Document**: Detailed design with diagrams and specifications
- **Proof of Concept**: Working prototype or benchmark results
- **Recommendations**: Clear next steps and action items

## Related Documents

- [Research Tasks Overview](tasks/README.md) - All 17 research tasks with priorities
- [GitHub Issues](https://github.com/ValueRetail/vrsky/issues) - Task tracking
- [README.md](../README.md) - Project overview and current status

## Document History

| Date       | Version | Changes                                                 |
| ---------- | ------- | ------------------------------------------------------- |
| 2026-01-27 | 1.0     | Initial inception document created from session prompts |

---

**This document captures the original vision and requirements that shaped the VRSky platform. It should be referenced when making architectural decisions to ensure alignment with the core principles.**
