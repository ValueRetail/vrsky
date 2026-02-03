# File Consumer & Producer Configuration Guide

## Overview

This document provides comprehensive documentation for the VRSky File Consumer and File Producer components, including environment variables, configuration options, and usage examples.

## File Consumer (FileConsumer)

The File Consumer monitors a directory for new files and creates envelope messages that can be processed by the integration pipeline.

### Environment Variables

#### FILE_INPUT_DIR
- **Description**: The directory to monitor for incoming files
- **Type**: String (file path)
- **Default**: `/tmp/file-input`
- **Required**: No
- **Example**: `/data/incoming` or `/home/user/uploads`

#### FILE_INPUT_PATTERN
- **Description**: Glob pattern for filtering which files to process
- **Type**: String (glob pattern)
- **Default**: `*` (process all files)
- **Required**: No
- **Examples**:
  - `*` - All files
  - `*.txt` - Only text files
  - `*.json` - Only JSON files
  - `data-*.csv` - Files starting with "data-" and ending with ".csv"
  - `{*.json,*.xml}` - Either JSON or XML files

#### FILE_INPUT_POLL_INTERVAL
- **Description**: How often to check the directory for new files
- **Type**: Duration string (Go duration format)
- **Default**: `5s` (5 seconds)
- **Required**: No
- **Valid formats**:
  - Milliseconds: `100ms`, `500ms`
  - Seconds: `1s`, `5s`, `30s`
  - Minutes: `1m`, `5m`
- **Notes**:
  - Lower intervals (e.g., `100ms`) mean faster file detection but higher CPU usage
  - Higher intervals (e.g., `1m`) mean lower resource usage but slower detection
  - Recommended: `5s` for most use cases

### File Type Detection

The File Consumer automatically detects content types based on file extensions:

| Extension | MIME Type |
|-----------|-----------|
| `.txt` | `text/plain` |
| `.json` | `application/json` |
| `.xml` | `application/xml` |
| `.csv` | `text/csv` |
| `.pdf` | `application/pdf` |
| `.html` | `text/html` |
| `.css` | `text/css` |
| `.js` | `application/javascript` |
| `.yaml` / `.yml` | `application/x-yaml` |
| `.zip` | `application/zip` |
| *(unknown)* | `application/octet-stream` |

### Envelope Structure

Each file creates an envelope with the following structure:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "tenant_id": "",
  "integration_id": "",
  "payload": "<file contents as bytes>",
  "payload_ref": "",
  "payload_size": 1024,
  "content_type": "text/plain",
  "source": "file-consumer",
  "current_step": 0,
  "step_history": [],
  "created_at": "2026-02-03T12:00:00Z",
  "expires_at": "2026-02-03T12:15:00Z",
  "retry_count": 0,
  "last_error": ""
}
```

### Example Configuration

```bash
# Monitor /data/incoming for all files every 5 seconds
export FILE_INPUT_DIR=/data/incoming
export FILE_INPUT_PATTERN=*
export FILE_INPUT_POLL_INTERVAL=5s

# Or monitor specifically for JSON files
export FILE_INPUT_DIR=/data/webhooks
export FILE_INPUT_PATTERN=*.json
export FILE_INPUT_POLL_INTERVAL=1s

# Fast polling for high-volume scenarios
export FILE_INPUT_DIR=/data/fast-ingestion
export FILE_INPUT_PATTERN=*
export FILE_INPUT_POLL_INTERVAL=100ms
```

## File Producer (FileProducer)

The File Producer writes envelope contents to the file system, creating files with configurable naming patterns and permissions.

### Environment Variables

#### FILE_OUTPUT_DIR
- **Description**: The directory where output files will be written
- **Type**: String (file path)
- **Default**: `/tmp/file-output`
- **Required**: No
- **Notes**:
  - Directory will be created if it doesn't exist
  - Ensure proper write permissions
- **Example**: `/data/outgoing` or `/home/user/exports`

#### FILE_OUTPUT_FILENAME_FORMAT
- **Description**: Template for generating output filenames
- **Type**: String (Go text/template format)
- **Default**: `{{.ID}}.{{.Extension}}`
- **Required**: No
- **Available template variables**:
  - `{{.ID}}` - The envelope ID (UUID)
  - `{{.Extension}}` - File extension derived from content type
  - `{{.TenantID}}` - Tenant ID (if set in envelope)
  - `{{.IntegrationID}}` - Integration ID (if set in envelope)
  - `{{.Source}}` - Source component name

**Template Examples**:
```bash
# Simple format: ID.ext
FILE_OUTPUT_FILENAME_FORMAT="{{.ID}}.{{.Extension}}"
# Output: 550e8400-e29b-41d4-a716-446655440000.json

# With timestamp-like IDs
FILE_OUTPUT_FILENAME_FORMAT="output-{{.ID}}.{{.Extension}}"
# Output: output-550e8400-e29b-41d4-a716-446655440000.json

# With source prefix
FILE_OUTPUT_FILENAME_FORMAT="{{.Source}}-{{.ID}}.{{.Extension}}"
# Output: file-consumer-550e8400-e29b-41d4-a716-446655440000.json

# With directory structure (tenant isolation)
FILE_OUTPUT_FILENAME_FORMAT="{{.TenantID}}/{{.IntegrationID}}/{{.ID}}.{{.Extension}}"
# Output: tenant-123/integration-456/550e8400-e29b-41d4-a716-446655440000.json
```

#### FILE_OUTPUT_PERMISSIONS
- **Description**: Unix file permissions for created files (octal format)
- **Type**: String (octal)
- **Default**: `0644` (owner read/write, others read)
- **Required**: No
- **Common values**:
  - `0644` - Owner read/write, others read (default, most common)
  - `0600` - Owner read/write only (restricted, for sensitive data)
  - `0755` - Owner read/write/execute, others read/execute
  - `0777` - Everyone can read/write/execute (not recommended)

### Extension Detection

The File Producer derives file extensions from the envelope's `ContentType`:

| Content Type | Extension |
|--------------|-----------|
| `text/plain` | `txt` |
| `application/json` | `json` |
| `application/xml` | `xml` |
| `text/csv` | `csv` |
| `application/pdf` | `pdf` |
| `text/html` | `html` |
| `text/css` | `css` |
| `application/javascript` | `js` |
| `application/x-yaml` | `yaml` |
| `application/zip` | `zip` |
| *(unknown)* | `bin` |

### Security Features

1. **Path Traversal Prevention**
   - Filenames with `..` or `/` are sanitized
   - Output files are always contained within `FILE_OUTPUT_DIR`
   - Example: ID `../../etc/passwd` becomes a sanitized filename

2. **Character Sanitization**
   - Unsafe filename characters are removed or replaced
   - Prevented characters: `\x00` (null), control characters
   - Special handling: `/`, `\`, `:` are sanitized

3. **Permission Control**
   - Files are created with specified permissions
   - Prevents unauthorized access to sensitive output

### Example Configuration

```bash
# Basic output
export FILE_OUTPUT_DIR=/data/outgoing
export FILE_OUTPUT_FILENAME_FORMAT="{{.ID}}.{{.Extension}}"
export FILE_OUTPUT_PERMISSIONS=0644

# Restricted permissions for sensitive data
export FILE_OUTPUT_DIR=/data/sensitive-output
export FILE_OUTPUT_FILENAME_FORMAT="secure-{{.ID}}.{{.Extension}}"
export FILE_OUTPUT_PERMISSIONS=0600

# Multi-tenant output with directory structure
export FILE_OUTPUT_DIR=/data/multi-tenant
export FILE_OUTPUT_FILENAME_FORMAT="{{.TenantID}}/{{.IntegrationID}}/{{.ID}}.{{.Extension}}"
export FILE_OUTPUT_PERMISSIONS=0644

# With source tracking
export FILE_OUTPUT_DIR=/data/processed
export FILE_OUTPUT_FILENAME_FORMAT="{{.Source}}-{{.ID}}-{{.TenantID}}.{{.Extension}}"
export FILE_OUTPUT_PERMISSIONS=0644
```

## Integration Scenarios

### Scenario 1: Web Form to File Export

```bash
# Consumer: HTTP webhook receives form submissions
export FILE_INPUT_DIR=/tmp/file-input

# Producer: Export processed data to shared folder
export FILE_OUTPUT_DIR=/mnt/shared/exports
export FILE_OUTPUT_FILENAME_FORMAT="export-{{.ID}}.csv"
export FILE_OUTPUT_PERMISSIONS=0644
```

### Scenario 2: API Polling with File Storage

```bash
# Consumer: Periodic API polling writes to temp directory
export FILE_INPUT_DIR=/tmp/api-responses
export FILE_INPUT_PATTERN=*.json
export FILE_INPUT_POLL_INTERVAL=5s

# Producer: Archive processed responses
export FILE_OUTPUT_DIR=/data/api-archive
export FILE_OUTPUT_FILENAME_FORMAT="{{.Source}}-{{.CreatedAt}}-{{.ID}}.{{.Extension}}"
export FILE_OUTPUT_PERMISSIONS=0644
```

### Scenario 3: Multi-Tenant SaaS Platform

```bash
# Consumer: One shared upload directory (monitored)
export FILE_INPUT_DIR=/mnt/uploads
export FILE_INPUT_PATTERN=*
export FILE_INPUT_POLL_INTERVAL=1s

# Producer: Organize by tenant and integration
export FILE_OUTPUT_DIR=/mnt/processed
export FILE_OUTPUT_FILENAME_FORMAT="{{.TenantID}}/{{.IntegrationID}}/{{.ID}}.{{.Extension}}"
export FILE_OUTPUT_PERMISSIONS=0600  # Tenant-isolated access
```

## Testing

### Run Unit Tests
```bash
cd /home/ludvik/vrsky/src
/home/ludvik/go/bin/go test -v ./pkg/io -run FileConsumer -timeout 15s
/home/ludvik/go/bin/go test -v ./pkg/io -run FileProducer -timeout 15s
```

### Run Integration Tests
```bash
/home/ludvik/go/bin/go test -v ./pkg/io -run Pipeline -timeout 15s
```

### Run E2E Test Suite
```bash
bash /home/ludvik/vrsky/test/e2e_file_components.sh
```

### Manual Testing
```bash
bash /home/ludvik/vrsky/test/manual_testing_guide.sh
```

## Troubleshooting

### Files Not Being Detected

1. **Check directory permissions**
   ```bash
   ls -ld $FILE_INPUT_DIR
   # Should have 'r' and 'x' permissions for user
   ```

2. **Verify file pattern**
   ```bash
   # Test glob pattern
   ls $FILE_INPUT_DIR/$FILE_INPUT_PATTERN
   ```

3. **Check poll interval**
   - Increase interval to ensure files aren't skipped
   - Check logs for processing errors

### Output Files Not Created

1. **Verify output directory**
   ```bash
   ls -ld $FILE_OUTPUT_DIR
   # Should have 'w' and 'x' permissions
   ```

2. **Check filename format**
   - Ensure template is valid
   - Check logs for template errors

3. **Verify permissions**
   - Ensure `FILE_OUTPUT_PERMISSIONS` is valid octal
   - Should be between 0000 and 0777

### Performance Issues

1. **Too many files**
   - Increase `FILE_INPUT_POLL_INTERVAL` (e.g., from `1s` to `5s`)
   - Use more specific `FILE_INPUT_PATTERN`

2. **High CPU usage**
   - Increase poll interval
   - Move old processed files from input directory

3. **Disk space**
   - Monitor output directory size
   - Implement cleanup policy for output files

## API Reference

### FileConsumer Interface

```go
type Consumer interface {
    Start(ctx context.Context) error
    Read(ctx context.Context) (*envelope.Envelope, error)
    Close() error
}
```

### FileProducer Interface

```go
type Producer interface {
    Start(ctx context.Context) error
    Write(ctx context.Context, env *envelope.Envelope) error
    Close() error
}
```

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 1.0.0 | 2026-02-03 | Initial release - File Consumer & Producer |

## Support

For issues, questions, or feature requests:
- Check the logs for detailed error messages
- Review the test files for usage examples
- Run the E2E test suite to verify your configuration
