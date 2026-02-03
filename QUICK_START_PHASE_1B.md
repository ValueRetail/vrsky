# Phase 1B Quick Start Guide

## 🚀 30-Second Overview

**What:** HTTP Consumer that receives webhooks and forwards to NATS  
**Where:** `/home/ludvik/vrsky/src/cmd/consumer/basic/`  
**Status:** ✅ Ready to build and test  

## ⚡ Quick Commands

```bash
cd /home/ludvik/vrsky

# Build
make build-consumer

# Test (requires Go compiler)
make test

# Run locally
docker run -d -p 4222:4222 nats:latest  # Start NATS
make run-consumer                        # Start consumer

# Test with webhook (in another terminal)
curl -X POST http://localhost:8000/webhook \
  -H "Content-Type: application/json" \
  -d '{"order_id":"123"}'  # Returns 202 ✓

# Full pipeline test (from vrsky root)
./scripts/e2e-test.sh  # Tests: HTTP → Consumer → NATS → Producer → HTTP
```

## 📁 Key Files Created

- `src/pkg/io/http_input.go` - HTTP webhook receiver
- `src/pkg/io/nats_output.go` - NATS publisher  
- `src/cmd/consumer/basic/main.go` - Entry point
- `src/cmd/consumer/Dockerfile` - Docker build
- `src/pkg/io/http_input_test.go` - Unit tests
- `src/pkg/io/nats_output_test.go` - Unit tests
- `src/pkg/io/e2e_integration_test.go` - Full pipeline test
- `README_CONSUMER.md` - Full documentation
- `PHASE_1B_SUMMARY.md` - Detailed summary

## 🔧 Configuration

```bash
# Set these environment variables before running:
export INPUT_TYPE=http
export INPUT_CONFIG='{"port":"8000"}'
export OUTPUT_TYPE=nats
export OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"test.messages"}'
```

## ✅ Testing Checklist

- [ ] `make build-consumer` - Compiles successfully
- [ ] `make test` - All unit tests pass
- [ ] `make e2e-test` - Full pipeline works
- [ ] Custom webhook - Send test data to :8000/webhook

## 📊 Message Flow

```
HTTP :8000/webhook
    ↓ (POST JSON)
Consumer accepts (202)
    ↓
Creates Envelope
    ↓
Publishes to NATS
    ↓
Producer subscribes & receives
    ↓
Sends to HTTP endpoint ✓
```

## 🐛 Troubleshooting

**Port 8000 in use?**
```bash
lsof -i :8000
kill -9 <PID>
```

**NATS not running?**
```bash
docker run -d -p 4222:4222 nats:latest
```

**Build fails?**
```bash
cd src
go mod tidy
make clean
make build-consumer
```

## 📖 Full Documentation

See `README_CONSUMER.md` for comprehensive guide including:
- Architecture diagrams
- All configuration options
- Docker deployment
- Complete troubleshooting

See `PHASE_1B_SUMMARY.md` for implementation details.

## 🎯 Next Steps

1. Build: `make build-consumer`
2. Test: `make e2e-test`
3. Deploy: `make docker-build-consumer`
4. Next Phase: Phase 2 Consumer or Phase 1C Converter

---

**Status:** ✅ Phase 1B Complete  
**Ready:** Testing & Deployment  
**Questions:** See README_CONSUMER.md
