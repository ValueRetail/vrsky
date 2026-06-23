# VRSky Integration Platform

> A highly scalable, cloud-native integration hub designed to connect internal and external systems through a marketplace-driven ecosystem.

![Status](https://img.shields.io/badge/status-research-blue)
![License](https://img.shields.io/badge/license-Fair_Source-orange)
![Commercial](https://img.shields.io/badge/commercial-license_required-red)

## Documentation

Full documentation lives in [`docs/`](docs/index.md) and builds into a site
(mkdocs):

- **[Build your first pipeline](docs/tutorials/first-pipeline.md)** — ≤10 min, no code
- **[Build your first connector](docs/sdk/tutorial/first-connector.md)** — ≤30 min, on the SDK
- **[Operator guide](docs/operator/install.md)** — install, upgrade, backup, monitoring, troubleshooting
- **[Connectors](docs/connectors/index.md)** — one page per connector
- **[Security whitepaper](docs/security/whitepaper.md)**
- **[API reference](docs/reference/api.md)** — OpenAPI at `/openapi.json`, Swagger UI at `/docs`

## Quick start

```bash
cp .env.example .env          # set ENCRYPTION_KEY (openssl rand -hex 32) + passwords
docker compose up -d --build  # start the stack (detached)
cd ui && npm install && npm run dev   # UI at http://localhost:5173
```

Then follow the [first-pipeline tutorial](docs/tutorials/first-pipeline.md). See
the [install guide](docs/operator/install.md) for production (Kubernetes).

## Vision

VRSky is an integration platform as a service (iPaaS) that revolutionizes how organizations connect their systems. By combining the power of modern message streaming with a thriving connector marketplace, VRSky enables seamless data flow between applications, services, and partners.

### Key Differentiators

- **Ephemeral by Design**: No persistent storage in the platform core - messages only live during transit
- **Reference-Based Messaging**: Large payloads stored efficiently in object storage, with NATS carrying lightweight references
- **Multi-Tenant with Collaboration**: Strong isolation with controlled cross-tenant data sharing for B2B scenarios
- **Marketplace Economy**: Developers can publish and monetize connectors, creating a vibrant ecosystem
- **Measured Throughput**: Go + NATS, **measured at ~15,000 msg/s sustained end-to-end on a single dev host (p99 ≈ 20 ms, zero loss)** — over a billion messages/day with sub-100 ms latency, with headroom to scale out horizontally. See [docs/LOAD.md](docs/LOAD.md) for the numbers and how to reproduce them.

## Architecture Philosophy

```
┌─────────────────────────────────────────────────────────────┐
│                    VRSky Platform Core                      │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐               │
│  │ Consumer │───▶│ Converter│───▶│ Producer │     Ephemeral │
│  │          │    │  Filter  │    │          │     Processing│
│  └──────────┘    └──────────┘    └──────────┘               │
│         │              │               │                    │
│         └──────────────┴───────────────┘                    │
│                        │                                    │
│                   NATS JetStream                            │
│              (Reference-Based Messaging)                    │
└─────────────────────────────────────────────────────────────┘
                           │
        ┌──────────────────┴──────────────────┐
        │                                     │
┌───────▼────────┐                   ┌────────▼─────────┐
│ Object Storage │                   │  Storage-as-a-   │
│   (Temporary)  │                   │  Service (Opt-in)│
│                │                   │                  │
│ • Large files  │                   │ • Message archive│
│ • Auto-cleanup │                   │ • State persist  │
│ • Pre-signed   │                   │ • Compliance     │
└────────────────┘                   └──────────────────┘
```

### Core Concepts

**Consumers**: Ingest data from external systems (APIs, databases, webhooks, queues)

**Producers**: Deliver data to target systems (APIs, databases, storage, notifications)

**Converters**: Transform data between formats (JSON↔XML, mapping, enrichment)

**Filters**: Route, filter, and process messages based on rules and conditions

**Marketplace**: Discover, install, and monetize pre-built connectors

**Storage-as-a-Service**: Optional paid add-on for long-term message archival and state persistence

## Technology Stack (Proposed)

| Component         | Technology                           | Rationale                                         |
| ----------------- | ------------------------------------ | ------------------------------------------------- |
| **Backend**       | Go                                   | Superior concurrency, low footprint, cloud-native |
| **Messaging**     | NATS + JetStream                     | 11M+ msgs/sec, multi-tenancy, persistence options |
| **Storage**       | S3/GCS/Azure Blob                    | Scalable object storage for large payloads        |
| **Orchestration** | Kubernetes                           | Container orchestration, auto-scaling             |
| **Observability** | OpenTelemetry + Prometheus + Grafana | Metrics, logs, traces                             |
| **API Gateway**   | TBD (Kong/Envoy/Traefik)             | Under research                                    |

> Note: These technologies are being validated through research. See [Research Tasks](#current-phase-research) below.

## Use Cases

### B2B Data Exchange

Connect with suppliers, partners, and customers securely with fine-grained permissions and audit trails.

### Enterprise Integration

Break down data silos by connecting legacy systems, SaaS applications, and modern microservices.

### Event-Driven Architecture

Build reactive systems that respond to events across your entire technology stack.

### Marketplace Ecosystem

Developers create and monetize connectors while enterprises benefit from pre-built integrations.

### Multi-Cloud Strategy

Integrate applications across AWS, GCP, Azure, and on-premise infrastructure.

## Current Phase: Research

We're currently in the research phase, evaluating technologies and designing the architecture. Our research is organized into 17 comprehensive tasks:

**📋 View all research tasks**: [docs/tasks/README.md](docs/tasks/README.md)

**📝 Project inception and original vision**: [docs/PROJECT_INCEPTION.md](docs/PROJECT_INCEPTION.md)

**🔗 Track progress**: [GitHub Issues](https://github.com/ValueRetail/vrsky/issues)

### Research Priorities

**P0 - Critical Foundation**

- Technology stack evaluation (.NET vs Go)
- Message transport architecture (NATS design)
- Core platform architecture
- Multi-tenancy and data isolation

**P1 - Core Platform**

- Component model (consumers, producers, converters, filters)
- Plugin/connector SDK design
- Security and authentication
- API gateway and service mesh
- Orchestration engine

**P2 - Business Layer**

- Marketplace platform
- Storage-as-a-Service
- Cross-tenant collaboration

**P3 - Operations & Quality**

- Observability and monitoring
- Deployment and infrastructure
- Performance testing
- Developer experience
- Documentation

**Estimated Timeline**: 2 weeks research + 9 weeks development = **11 weeks to POC**

## Getting Started (Coming Soon)

Once we complete the research phase, we'll provide:

- Quick start guide
- Local development setup
- SDK installation
- Example integrations
- Connector development guide

## Implemented Components

### Phase 1C: File-Based Integration (✅ Complete)

We've successfully implemented the **File Consumer** and **File Producer** components:

#### File Consumer
Monitors a directory for incoming files and creates envelope messages for pipeline processing.

**Features:**
- Directory monitoring with configurable poll intervals
- Glob pattern support for file filtering
- Automatic content-type detection
- Metadata enrichment (ID, timestamps, source tracking)

**Configuration:**
```bash
export FILE_INPUT_DIR=/data/incoming
export FILE_INPUT_PATTERN=*.json
export FILE_INPUT_POLL_INTERVAL=5s
```

#### File Producer
Writes envelope contents to files with configurable naming and permissions.

**Features:**
- Template-based filename generation
- Configurable file permissions
- Path traversal prevention
- Extension detection from content-type

**Configuration:**
```bash
export FILE_OUTPUT_DIR=/data/outgoing
export FILE_OUTPUT_FILENAME_FORMAT="{{.ID}}.{{.Extension}}"
export FILE_OUTPUT_PERMISSIONS=0644
```

**📚 Complete Documentation**: [docs/FILE_CONSUMER_PRODUCER_CONFIG.md](docs/FILE_CONSUMER_PRODUCER_CONFIG.md)

**🧪 Test Suite**: 
- 20+ unit tests (100% passing)
- 8 integration tests
- E2E test script: `bash test/e2e_file_components.sh`

**📋 Source Code**:
- `src/pkg/io/file_input.go` - File Consumer implementation
- `src/pkg/io/file_output.go` - File Producer implementation
- `src/cmd/file-consumer/main.go` - Consumer entry point
- `src/cmd/file-producer/main.go` - Producer entry point

## Contributing

We're in the early research phase. If you'd like to contribute:

1. Review the [research tasks](docs/tasks/README.md)
2. Comment on relevant [GitHub issues](https://github.com/ValueRetail/vrsky/issues)
3. Share your expertise and experience with similar platforms
4. Propose additional research areas we should consider

## Project Status

**🚀 Aggressive Timeline - POC by Mid-April 2026**

| Milestone              | Status         | Target Date      | Duration           |
| ---------------------- | -------------- | ---------------- | ------------------ |
| Research Phase         | 🔵 In Progress | Jan 27 - Feb 9   | **2 weeks**        |
| Core Development       | ⚪ Planned     | Feb 10 - Mar 23  | 6 weeks            |
| Integration & Testing  | ⚪ Planned     | Mar 24 - Apr 7   | 2 weeks            |
| Documentation & Polish | ⚪ Planned     | Apr 8 - Apr 15   | 1 week             |
| **POC Release**        | ⚪ Planned     | **Apr 15, 2026** | **11 weeks total** |
| Alpha Release          | ⚪ Future      | Q3 2026          | TBD                |
| Production Release     | ⚪ Future      | Q4 2026          | TBD                |

**See detailed timeline**: [docs/ACCELERATED_TIMELINE.md](docs/ACCELERATED_TIMELINE.md)

## License

VRSky is licensed under the **Fair Source License** (1 user).

### Free Use ✅

**FREE** for:

- **Personal use** (1 user, internal projects only)
- **Educational institutions** (unlimited, internal use only)
- **Non-profit organizations** (unlimited, internal use only)

⚠️ **Internal Use Only** - Free licenses do NOT permit offering VRSky as a service to others.

### Commercial Use 💰

**Commercial license required** for:

- Companies with 2+ users
- Production commercial deployments
- Internal business integrations

⚠️ **Service Provider Use Prohibited** - Standard commercial licenses are for internal use only.

**Service Provider License required** for:

- Offering VRSky as a managed/hosted service
- Building SaaS/iPaaS platforms using VRSky
- Multi-tenant service provider deployments
- Reselling VRSky access to customers

**[View Commercial License Details →](COMMERCIAL_LICENSE.md)**

### Delayed Open Source Publication ⏰

VRSky follows Fair Source principles: **each version becomes Open Source (Apache 2.0) two years after its release** or when discontinued. This ensures long-term community availability while supporting sustainable development.

### Summary

VRSky is **source-available** software. The code is publicly available on GitHub, but commercial use with multiple users requires a paid license. This ensures the project remains sustainable while being freely available for personal, educational, and non-profit use.

**License**: [Fair Source License](LICENSE)  
**Commercial**: Contact sales@valueretail.com

## Contact & Community

- **Issues**: [GitHub Issues](https://github.com/ValueRetail/vrsky/issues)
- **Discussions**: [GitHub Discussions](https://github.com/ValueRetail/vrsky/discussions)
- **Research Tasks**: [docs/tasks/README.md](docs/tasks/README.md)

---

**Built with ❤️ by the ValueRetail team**
