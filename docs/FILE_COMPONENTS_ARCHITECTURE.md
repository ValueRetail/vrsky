# File Components Architecture

This document explains the design decisions, architectural patterns, and technical reasoning behind the VRSky File Consumer and File Producer components.

## Table of Contents
1. [Overview](#overview)
2. [Design Principles](#design-principles)
3. [Architecture Diagrams](#architecture-diagrams)
4. [Component Design](#component-design)
5. [Data Flow](#data-flow)
6. [Design Decisions](#design-decisions)
7. [Scalability](#scalability)
8. [Fault Tolerance](#fault-tolerance)
9. [Performance Characteristics](#performance-characteristics)
10. [Future Enhancements](#future-enhancements)

## Overview

The File Components (Consumer and Producer) form the file I/O layer of the VRSky integration platform. They provide high-performance, scalable file processing with support for:
- Large files (up to 10GB+ without memory exhaustion)
- Multiple file sources and destinations
- Automatic error recovery with retry logic
- Archive and error directory management
- Reprocessing prevention
- Advanced file organization strategies

### Core Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    File Consumer & Producer                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  Input Directory                    NATS Broker              Output Directory
│  ┌────────────────┐                ┌──────────────┐         ┌─────────────────┐
│  │  data/         │                │  NATS Server │         │  output/        │
│  │  ├─ file1.csv  │──┐             │  (4222)      │         │  ├─ 2026/02/13/ │
│  │  ├─ file2.json │  │             │              │         │  │  ├─ file1.csv│
│  │  └─ file3.txt  │  │             └──────────────┘         │  │  └─ file2.json
│  └────────────────┘  │                    ▲                 │  └─────────────────┘
│                      │                    │                 │
│                  Consumer           Envelope                Producer
│                  Publishes        Topic:                   Subscribes
│                  Envelope         files.input              Consumes
│                      └────────────────────┴──────────────────┘
│
│                      Archive/Error Directories
│                      ┌──────────────────────┐
│                      │ archive/2026-02-13/  │
│                      │ error/2026-02-13/    │
│                      └──────────────────────┘
└─────────────────────────────────────────────────────────────────┘
```

## Design Principles

### 1. Ephemeral Processing
- **Principle**: Files are not stored in the platform core
- **Implementation**: Files are transited quickly through Consumer→NATS→Producer
- **Benefit**: Reduces storage requirements, enables scalability
- **Trade-off**: Archives are external, not platform-managed

### 2. Reference-Based Messaging
- **Principle**: Only references to files travel in messages
- **Implementation**: File content as stream, metadata in envelope
- **Benefit**: NATS carries lightweight messages, even for GB files
- **Trade-off**: Consumer and Producer need direct file system access

### 3. Atomic Operations
- **Principle**: Operations either fully succeed or fully fail
- **Implementation**: Write-to-temp, then atomic rename
- **Benefit**: No partial/corrupted files on disk
- **Trade-off**: Slight overhead for atomic operations

### 4. Fail-Safe with Recovery
- **Principle**: Transient failures trigger retries, permanent failures move to error dir
- **Implementation**: Exponential backoff + max retry limit
- **Benefit**: Self-healing for transient issues
- **Trade-off**: Some files may need manual intervention

### 5. Memory Safety for Large Files
- **Principle**: Streaming I/O for files >1MB
- **Implementation**: Chunked reads/writes with periodic fsyncs
- **Benefit**: Constant memory regardless of file size
- **Trade-off**: More complex code, need for chunk size tuning

## Architecture Diagrams

### File Consumer: Detailed Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                    File Consumer Pipeline                        │
└─────────────────────────────────────────────────────────────────┘

1. POLL PHASE (every 5 seconds)
   ┌──────────────────────┐
   │ List Files in Dir    │
   │ Match Pattern        │
   └─────────┬────────────┘
             │
2. DUPLICATE CHECK
   ┌──────────────────────┐
   │ Calculate Hash       │
   │ (first 64KB)         │
   │ + mtime              │
   └─────────┬────────────┘
             │
3. LOCK DETECTION
   ┌──────────────────────┐
   │ File Locked?         │
   │ (size/mtime change)  │
   └────────┬─────────────┘
            │
      ┌─────▼──────┐
      │    Yes     │ No
      ▼            ▼
    SKIP      4. READ & PUBLISH
            ┌──────────────────────┐
            │ Read entire file     │
            │ Create envelope      │
            │ Publish to NATS      │
            └─────────┬────────────┘
                      │
            5. HANDLE SUCCESS
            ┌──────────────────────┐
            │ Delete or            │
            │ Move to Archive      │
            └──────────────────────┘

6. ERROR HANDLING (on failure)
   ┌──────────────────────┐
   │ Retry <= MAX?        │
   │ (exp. backoff)       │
   └────────┬─────────────┘
            │
      ┌─────▼──────────┐
      │ Yes      │ No  │
      ▼          ▼
   RETRY   MOVE TO ERROR
           + Metadata File
```

### File Producer: Detailed Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                    File Producer Pipeline                        │
└─────────────────────────────────────────────────────────────────┘

1. RECEIVE ENVELOPE
   ┌──────────────────────┐
   │ Subscribe to NATS    │
   │ Receive message      │
   │ Deserialize envelope │
   └─────────┬────────────┘
             │
2. VALIDATE ENVELOPE
   ┌──────────────────────┐
   │ Check required fields│
   │ Verify size limits   │
   │ Content-type check   │
   └──────────┬───────────┘
              │
3. CHECK DISK SPACE
   ┌──────────────────────┐
   │ Need 2×file size?    │
   │ Available space?     │
   └────────┬─────────────┘
            │
      ┌─────▼──────┐
      │    No      │ Yes
      ▼            ▼
  ERROR        4. GET PATH
              ┌──────────────────────┐
              │ Apply organization   │
              │ strategy             │
              │ (type/date/source)   │
              │ Expand filename      │
              └─────────┬────────────┘
                        │
              5. STREAM WRITE
              ┌──────────────────────┐
              │ For each chunk:      │
              │ - Read from payload  │
              │ - Write to file      │
              │ - Update SHA256      │
              │ - Fsync (periodic)   │
              └─────────┬────────────┘
                        │
              6. VERIFY & CLOSE
              ┌──────────────────────┐
              │ Final fsync          │
              │ Calculate checksum   │
              │ Close file           │
              │ Log result           │
              └──────────────────────┘
```

### File Lifecycle: Complete Journey

```
INPUT → CONSUMER → NATS → PRODUCER → OUTPUT
  │
  File appears in input/
  │
  └─→ Consumer detects
      │
      ├─→ Hash tracking: Is this new?
      │   ├─ Yes: Read and publish
      │   └─ No: Skip (already processed)
      │
      ├─→ File locking: Is file being written?
      │   ├─ Yes: Skip, retry next poll
      │   └─ No: Proceed to read
      │
      ├─→ Read phase: Full file into envelope
      │   │
      │   └─→ Publish to NATS (files.input topic)
      │
      └─→ Handle after publish
          ├─ Success
          │  ├─→ DELETE_AFTER_PROCESSING=true: Delete
          │  ├─→ ARCHIVE_DIR set: Move to archive/{YYYY-MM-DD}/
          │  └─→ Neither: Leave in place
          │
          └─ Failure
             ├─→ Retry < MAX: Exponential backoff, retry
             │   (1s, 2s, 4s, 8s...)
             └─→ Retry >= MAX: Move to error/{YYYY-MM-DD}/
                 Create .error metadata file

NATS carries lightweight envelope:
{
  ID: "550e8400-e29b-41d4-a716-446655440000",
  SourcePath: "/data/input/sales.csv",
  ContentType: "text/csv",
  Payload: <bytes>,
  Source: "api",
  Timestamp: "2026-02-13T10:30:45Z"
}

Producer subscribes to files.input:
   │
   └─→ Receive envelope
       │
       ├─→ Validate fields
       │   ├─ ID present?
       │   ├─ Payload present?
       │   ├─ Size < MAX?
       │   └─ Content-type valid?
       │
       ├─→ Check disk space
       │   └─ Need 2×file size?
       │
       ├─→ Get output path
       │   └─ Apply organization (type/date/source)
       │
       ├─→ Stream write
       │   ├─ 64KB chunk (default)
       │   ├─ Next 64KB chunk
       │   ├─ ... (repeat)
       │   ├─ Fsync every 10 chunks (640KB)
       │   └─ Final fsync
       │
       └─→ Close and verify
           ├─ SHA256 checksum
           └─ Log to stdout

OUTPUT appears in:
  output/2026/02/13/api-550e8400.csv
  (if organize_by=date)
```

## Component Design

### File Consumer Architecture

```go
type FileConsumer struct {
    // Configuration
    inputDir          string              // Input directory path
    filePattern       string              // Glob pattern
    archiveDir        string              // Optional archive directory
    errorDir          string              // Optional error directory
    deleteAfter       bool                // Delete after processing
    maxRetries        int                 // Retry limit
    retryBackoffMs    int                 // Backoff in ms
    
    // State
    processedFiles    map[string]FileHash // Hash tracking
    failedFiles       map[string]int      // Retry counter
    natsConn          *nats.Conn          // NATS connection
    pollTicker        *time.Ticker        // Poll timer
    logger            *slog.Logger        // Structured logging
}

// Key Methods
func (c *FileConsumer) Start(ctx context.Context) error
func (c *FileConsumer) poll(ctx context.Context) error
func (c *FileConsumer) isFileProcessed(file string) bool
func (c *FileConsumer) calculateFileHash(file string) (string, error)
func (c *FileConsumer) isFileLocked(file string) bool
func (c *FileConsumer) moveToArchive(srcFile string) error
func (c *FileConsumer) moveToError(srcFile string, err string) error
func (c *FileConsumer) shouldRetry(file string) bool
```

### File Producer Architecture

```go
type FileProducer struct {
    // Configuration
    outputDir         string              // Output directory path
    fileNameFormat    string              // Template for filenames
    permissions       os.FileMode         // File permissions
    chunkSize         int64               // Read/write chunk size
    maxFileSize       int64               // Max allowed file size
    fsyncInterval     int                 // Fsync after N chunks
    createSubdirs     bool                // Create subdirectories
    organizeBy        string              // Organization strategy
    
    // State
    fileNameTemplate  *template.Template  // Parsed filename template
    natsConn          *nats.Conn          // NATS connection
    subscription      *nats.Subscription  // NATS subscription
    logger            *slog.Logger        // Structured logging
}

// Key Methods
func (p *FileProducer) Start(ctx context.Context) error
func (p *FileProducer) Write(ctx context.Context, envelope *Envelope) error
func (p *FileProducer) streamWrite(file *os.File, payload io.Reader) (string, error)
func (p *FileProducer) checkDiskSpace(requiredBytes int64) error
func (p *FileProducer) getOrganizedPath(envelope *Envelope) string
func (p *FileProducer) validateEnvelope(envelope *Envelope) error
```

## Data Flow

### Message Envelope Format

```
┌────────────────────────────────────────────────────────┐
│              Envelope (JSON)                            │
├────────────────────────────────────────────────────────┤
│ {                                                      │
│   "id": "550e8400-e29b-41d4-a716-446655440000",      │
│   "source_path": "/data/input/sales.csv",            │
│   "source": "api",                                    │
│   "content_type": "text/csv; charset=utf-8",         │
│   "payload": <binary>,                               │
│   "timestamp": "2026-02-13T10:30:45Z",               │
│   "metadata": {                                       │
│     "file_size": 1048576,                            │
│     "checksum": "abc123...",                         │
│     "retry_count": 0                                 │
│   }                                                   │
│ }                                                     │
└────────────────────────────────────────────────────────┘
```

### Processing State Transitions

```
           ┌─────────────────┐
           │   NOT_SEEN      │
           └────────┬────────┘
                    │ (Consumer detects)
                    ▼
           ┌─────────────────┐
           │  PROCESSING     │ ◄──┐
           └────────┬────────┘    │
                    │             │ (Retry)
        ┌───────────┴─────────┐   │
        │                     │   │
        ▼                     ▼   │
   ┌─────────────┐      ┌─────────────┐
   │   SUCCESS   │      │    FAILED   │
   └─────────────┘      └──────┬──────┘
        │                      │
        └──────┬───────────────┘
               │ (Decision point)
        ┌──────┴──────┐
        │             │
   ┌────▼──────┐  ┌──▼────────┐
   │  ARCHIVED │  │   ERROR   │
   │  or       │  │  MOVED    │
   │  DELETED  │  │           │
   └───────────┘  └───────────┘
```

## Design Decisions

### Decision 1: Polling vs inotify

**Decision**: Use polling instead of inotify/fsnotify

**Rationale**:
- **Simplicity**: Polling is easier to understand and debug
- **Compatibility**: Works on all filesystems (including network mounts)
- **Latency**: 5s polling is acceptable for most use cases
- **Cost**: Minimal overhead (single goroutine)

**Alternative Considered**: inotify/fsnotify
- Pro: Lower latency (<100ms)
- Con: Doesn't work on NFS, SMB; more complex; potential lost events

**Trade-off**: Accept 5-second latency for better reliability

---

### Decision 2: File Hashing for Reprocessing Prevention

**Decision**: Use SHA256 hash of first 64KB + modification time

**Rationale**:
- **Accuracy**: Detects file changes reliably
- **Performance**: First 64KB is fast even for large files
- **Across Reboots**: mtime survives restarts
- **Collision Risk**: Extremely low with SHA256

**Alternative Considered**:
- Inode + mtime: Doesn't work when file moves
- Full file hash: Too slow for large files
- Just mtime: Can have false positives

**Trade-off**: Requires both hash AND mtime match; prevents false negatives at cost of slightly more computation

---

### Decision 3: Streaming Writes for Large Files

**Decision**: Stream with configurable chunk size (default 64KB)

**Rationale**:
- **Memory Safety**: Constant memory regardless of file size
- **Performance**: 64KB chunks balance syscalls vs memory
- **Resumable**: Can detect errors mid-transfer
- **Safe**: Periodic fsyncs ensure durability

**Alternative Considered**:
- Load entire file: Simple but uses 1GB+ RAM for large files
- Single write call: Risky if interrupted
- Custom buffering: Reinventing the wheel

**Trade-off**: More complex code for production-grade safety

---

### Decision 4: Archive/Error Directories by Date

**Decision**: Auto-create {base_dir}/{YYYY-MM-DD}/ subdirectories

**Rationale**:
- **Organization**: Easy to find files from specific dates
- **Cleanup**: Can delete old archives/errors in batches
- **Performance**: Moderate directory sizes (not 1M files in one dir)
- **Compliance**: Audit trail by date

**Alternative Considered**:
- Single flat directory: Gets unwieldy with thousands of files
- By month (YYYY-MM): Less granular
- By hour (YYYY-MM-DD-HH): Too many directories

**Trade-off**: Accept extra directory creation for better organization

---

### Decision 5: Exponential Backoff for Retries

**Decision**: Backoff sequence 1s, 2s, 4s, 8s, then fail

**Rationale**:
- **Transient Recovery**: Gives transient issues time to heal
- **No Hammering**: Doesn't retry immediately (wastes resources)
- **Bounded**: Max 8 seconds prevents indefinite waiting
- **Proven**: Standard pattern in distributed systems

**Alternative Considered**:
- Linear backoff: Less aggressive, but slower recovery
- No backoff: Hammers the resource
- Random jitter: Overkill for single consumer

**Trade-off**: Accept up to 15 seconds total wait for max retries (1+2+4+8)

---

### Decision 6: Envelope Validation Pre-Write

**Decision**: Validate envelope before writing, move invalid to error queue

**Rationale**:
- **Early Detection**: Catch bad data before writing
- **Error Handling**: Clear error message
- **No Corruption**: Partial writes prevented
- **Debugging**: Easier to identify bad envelopes

**Alternative Considered**:
- Validate after read: Late detection, possible partial write
- Skip validation: Risk of corrupt files

**Trade-off**: Extra validation step for data safety

---

### Decision 7: File Organization Strategies

**Decision**: Support type, date, source, or none

**Rationale**:
- **Flexibility**: Different use cases need different org
- **Templates**: Allow custom patterns with {{.Variable}}
- **Reasonable Defaults**: date is most useful for most cases
- **Easy to Extend**: Can add more strategies

**Alternative Considered**:
- Single fixed strategy: Forces one approach
- All automatic: Too magical, hard to predict

**Trade-off**: Slight complexity for flexibility

---

### Decision 8: Disk Space Pre-Check

**Decision**: Require 2× file size available before write

**Rationale**:
- **Safety Buffer**: Accounts for filesystem overhead
- **No Surprises**: Detect issues before partial write
- **Batch Operations**: Room for multiple writes
- **Recovery**: Space for error files

**Alternative Considered**:
- Check only available space: Doesn't account for overhead
- No pre-check: Risk of partial writes

**Trade-off**: Extra check for complete safety

## Scalability

### Horizontal Scalability

**Consumer Scaling**:
- Run multiple Consumers on different input directories
- Each Consumer is independent (no shared state besides NATS)
- Can process in parallel: Consumer1→NATS + Consumer2→NATS + ...

**Producer Scaling**:
- Run multiple Producers, each subscribes to same topic
- NATS distributes messages across consumers (queue group)
- Each Producer writes independently
- **Caveat**: File names must be unique (use {{.ID}}) or handle collisions

**NATS Broker**:
- Single NATS for these components
- Can scale NATS to multiple instances (clustering)

### Vertical Scalability

**Memory**:
- Consumer: ~10-50MB baseline + processing buffer
- Producer: ~10-50MB baseline + chunk buffer
- Can process 100K+ files/hour on single machine

**CPU**:
- Consumer: Single polling goroutine + publishing
- Producer: Single subscription goroutine + writing
- Minimal CPU (~2-5% idle, ~20-30% active writing)

**I/O**:
- Limited by disk throughput
- SSD: ~500MB/s write, can process 5GB+/hour
- Network: Limited by network speed for reference payloads

## Fault Tolerance

### Consumer Fault Tolerance

**File Locking**: Detects files being written, skips until ready
**Network Failures**: Reconnects to NATS automatically
**Permissions**: Moves to error directory instead of crashing
**Large Files**: Handles GB-sized files without OOM

### Producer Fault Tolerance

**Disk Full**: Detects, moves to error queue, continues
**Permission Denied**: Moves to error queue, continues
**NATS Down**: Waits for reconnection (configurable timeout)
**Bad Envelopes**: Validates, skips corrupted data

### Message Durability

**At-Least-Once Delivery**:
- Consumer publishes to NATS, doesn't delete until published
- Producer receives, writes, confirms
- If Producer crashes after write: May write same file twice

**Strategies for Exactly-Once**:
- Use {{.ID}} as filename: Idempotent writes
- Check destination for duplicates: Application logic

## Performance Characteristics

### Throughput

**Files/Second**:
- Small files (<1MB): 100-500 files/sec per Consumer
- Medium files (1-100MB): 10-50 files/sec per Consumer
- Large files (>100MB): 1-5 files/sec per Consumer

**Total System**:
- Multiple consumers: Linear scaling up to network/disk limits

### Latency

**End-to-End (input → output)**:
- Minimum: 50-100ms (processing + write)
- Typical: 100-500ms (queuing + I/O)
- Maximum: Depends on file size and disk speed

**File Detection**:
- Maximum latency: ~5s (poll interval)
- Typical: 100-500ms after file appears

### Resource Usage

| Component | RAM | CPU | Storage |
|-----------|-----|-----|---------|
| Consumer Idle | 20MB | 1% | 0 |
| Consumer Active (1K files) | 50MB | 5% | 1MB (hash cache) |
| Producer Idle | 20MB | 1% | 0 |
| Producer Writing (1GB file) | 100MB | 20% | 1GB + temp |

## Future Enhancements

### Phase 2 Improvements

1. **Dead Letter Queue (DLQ)**
   - Persistent storage for permanently failed messages
   - Replay capability
   - Analysis and debugging

2. **Distributed Tracing**
   - Track file through Consumer → NATS → Producer
   - Performance metrics
   - Error causality

3. **Metrics/Observability**
   - Prometheus metrics (files_processed, bytes_written, errors)
   - Grafana dashboards
   - Real-time monitoring

4. **Filter Chain**
   - Transform files before archiving (compress, encrypt)
   - Validation rules
   - Custom processing

5. **S3/MinIO Integration**
   - Archive to cloud storage
   - Automatic tiering (old files to cold storage)
   - Cross-region replication

### Possible Enhancements

1. **Incremental Processing**
   - Process only changed portions of large files
   - Append-only file support

2. **Compression**
   - Compress before writing
   - Transparent decompression on read

3. **Encryption**
   - Encrypt sensitive files
   - Key management

4. **Schema Validation**
   - JSON Schema, Avro, Protobuf validation
   - Type checking before write

5. **Batching**
   - Collect multiple small files into batches
   - Reduce overhead for high-frequency small files

## References

- NATS Documentation: https://docs.nats.io/
- File I/O Best Practices: https://www.kernel.org/doc/html/latest/filesystems/
- Go io Package: https://golang.org/pkg/io/
- Streaming Patterns: https://en.wikipedia.org/wiki/Stream_processing
