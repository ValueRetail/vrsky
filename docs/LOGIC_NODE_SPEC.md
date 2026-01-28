# Logic Node WASM Specification

**Document Version**: 1.0  
**Status**: Design Specification  
**Date**: January 28, 2026

## Overview

Logic Nodes (Converters and Filters) enable users to define custom business logic for data transformation and routing. To ensure security, performance, and multi-tenancy, this logic is executed within a **WebAssembly (WASM)** sandbox using the `wazero` runtime.

While the underlying engine is WASM, the primary developer experience is focused on **TypeScript/JavaScript**.

## Runtime Architecture

```
┌───────────────────────────────────────────────┐
│              Go Host (Service Node)           │
│                                               │
│  ┌───────────────┐      ┌──────────────────┐  │
│  │ Message Queue │ ──→  │   WASM Runtime   │  │
│  └───────────────┘      │     (wazero)     │  │
│          ↑              └────────┬─────────┘  │
│          │                       │            │
│  ┌───────┴───────┐      ┌────────▼─────────┐  │
│  │ Host Exports  │ ←──  │   User Logic     │  │
│  │ (API methods) │      │  (Compiled WASM) │  │
│  └───────────────┘      └──────────────────┘  │
└───────────────────────────────────────────────┘
```

## Supported Languages

| Language | Support Level | Compilation Toolchain |
|----------|---------------|-----------------------|
| **TypeScript** | **Primary** | `AssemblyScript` or `Javy` (QuickJS -> WASM) |
| **JavaScript** | **Primary** | `Javy` (QuickJS -> WASM) |
| **Go** | Experimental | TinyGo |
| **Rust** | Experimental | `wasm-pack` |

## TypeScript SDK Interface

Users write logic by implementing specific entry points. The VRSky SDK provides types and helper functions.

### 1. Converter Interface

Converters transform an input message into a new format.

```typescript
import { Message, Context, Converter } from "@vrsky/sdk";

export const convert: Converter = (msg: Message, ctx: Context): Message => {
  // 1. Parse Input
  const input = JSON.parse(String.fromCharCode(...msg.payload));

  // 2. Transform
  const output = {
    id: input.order_id,
    total: input.amount * 100,
    currency: "USD",
    customer: ctx.metadata.customerID
  };

  // 3. Return new Message
  return {
    ...msg,
    payload: String.toCharCodes(JSON.stringify(output)),
    contentType: "application/json"
  };
};
```

### 2. Filter Interface

Filters decide if a message should proceed or be dropped/routed elsewhere.

```typescript
import { Message, Context, Filter, FilterResult } from "@vrsky/sdk";

export const filter: Filter = (msg: Message, ctx: Context): FilterResult => {
  const input = JSON.parse(String.fromCharCode(...msg.payload));

  // Logic: Only process orders over $1000
  if (input.total_value > 1000) {
    return FilterResult.Pass;
  }
  
  // Logic: Drop invalid orders
  if (!input.valid) {
    return FilterResult.Drop;
  }

  return FilterResult.Drop;
};
```

## Host Functions (The API)

The Go host exposes these functions to the WASM environment. This is the **only** way WASM code interacts with the outside world.

### Logging & Diagnostics
- `host.log(level: string, msg: string)`: Write to system logs.
- `host.trace(key: string, value: string)`: Add attribute to distributed trace.

### Network (Restricted)
- `host.httpCall(method: string, url: string, headers: map, body: bytes)`: Make an external HTTP request.
  - *Restriction*: Only allowed if URL matches whitelisted patterns in integration config.

### State & Storage (If enabled)
- `host.kvGet(key: string)`: Retrieve value from ephemeral KV store.
- `host.kvSet(key: string, value: string, ttl: int)`: Set value.

### Context Access
- `host.getSecret(name: string)`: Retrieve a secret (e.g., API key).
- `host.getMetadata(key: string)`: Get message metadata.

## Memory & Data Passing

Since WASM memory is linear and isolated:

1. **Host -> Guest (Input)**:
   - Host writes Message payload into WASM memory.
   - Host calls the `convert` or `filter` exported function with pointer/length.

2. **Guest -> Host (Output)**:
   - Guest allocates memory for result.
   - Guest returns pointer/length to Host.
   - Host reads memory, then frees it.

**Optimization**: We will use `TinyGo` or `AssemblyScript` memory allocators to manage this efficiently without heavy GC overhead.

## Configuration & Limits

### Resource Limits (Configurable per Integration)
- **Max Execution Time**: Default `100ms`. Hard kill if exceeded.
- **Max Memory**: Default `16MB`. WASM traps if exceeded.
- **Max Call Depth**: To prevent stack overflow loops.

### Configuration Schema

```yaml
# integration.yaml
converter:
  type: wasm
  source: "./dist/logic.wasm"
  config:
    currency: "EUR"  # Passed to WASM context
  limits:
    memory: 32MB
    timeout: 200ms
```

## Compilation Pipeline

VRSky provides a CLI tool to manage compilation.

1. **Initialize**: `vrsky init logic --template typescript`
2. **Develop**: Write `index.ts`.
3. **Build**: `vrsky build`
   - Validates code.
   - Transpiles TypeScript -> WASM.
   - Optimizes binary size (`wasm-opt`).
4. **Publish**: `vrsky publish`
   - Uploads `.wasm` to VRSky artifact registry.

## Security Model

1. **Isolation**: `wazero` ensures no access to host OS (files, env vars, sockets) unless explicitly exported.
2. **Denial of Service**:
   - Infinite loops caught by instruction metering or timeout.
   - Memory exhaustion caught by runtime limits.
3. **Data Leakage**:
   - WASM instance is tied to a specific `Customer-ID` request scope.
   - Memory is cleared/recycled between executions.

## Example: Calling an External API (TypeScript)

```typescript
import { host, Message } from "@vrsky/sdk";

export function convert(msg: Message): Message {
  // 1. Get API Key securely
  const apiKey = host.getSecret("FOREX_API_KEY");

  // 2. Make HTTP call via Host
  const response = host.httpCall({
    method: "GET",
    url: "https://api.forex.com/rates?base=USD",
    headers: { "Authorization": `Bearer ${apiKey}` }
  });

  // 3. Use data
  const rates = JSON.parse(response.body);
  const input = JSON.parse(msg.payload);
  
  input.amount_eur = input.amount_usd * rates.EUR;

  msg.payload = JSON.stringify(input);
  return msg;
}
```
