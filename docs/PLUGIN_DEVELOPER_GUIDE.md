# VRSky Plugin Developer Guide

**Document Version**: 1.0  
**Status**: Draft  
**Date**: January 28, 2026

## Welcome Developers!

VRSky allows you to extend the platform in two ways:
1.  **Connection Plugins (Go)**: Build high-performance connectors for external systems (Salesforce, SAP, Databases, etc.).
2.  **Logic Plugins (TypeScript)**: Write custom data transformation and filtering logic.

---

## 🏗️ Part 1: Building a Connection Plugin (Go)

Connection plugins are Go applications that implement the VRSky RPC interface. They run as separate processes for maximum isolation and performance.

### Prerequisites
- Go 1.21+
- [VRSky SDK](https://github.com/ValueRetail/vrsky/pkg/sdk) (Coming Soon)

### Step 1: Initialize Project

```bash
mkdir vrsky-connector-example
cd vrsky-connector-example
go mod init github.com/myuser/vrsky-connector-example
go get github.com/ValueRetail/vrsky/pkg/connector
```

### Step 2: Implement the Interface

Create `main.go`:

```go
package main

import (
    "context"
    "fmt"
    "github.com/ValueRetail/vrsky/pkg/connector"
    "github.com/hashicorp/go-plugin"
)

type MyConnector struct{}

func (c *MyConnector) Metadata() connector.PluginMetadata {
    return connector.PluginMetadata{
        ID:      "example-connector",
        Version: "1.0.0",
        Name:    "Example Connector",
    }
}

func (c *MyConnector) Validate(ctx context.Context, config connector.ConnectionConfig) error {
    if _, ok := config.Settings["url"]; !ok {
        return fmt.Errorf("missing required setting: url")
    }
    return nil
}

func (c *MyConnector) Connect(ctx context.Context, config connector.ConnectionConfig) error {
    // Initialize connection logic here
    return nil
}

func (c *MyConnector) Send(ctx context.Context, msg connector.Message) (string, error) {
    // Logic to send data to external system
    return "msg-123", nil
}

// ... Implement Receive, Disconnect, Health ...

func main() {
    plugin.Serve(&plugin.ServeConfig{
        HandshakeConfig: connector.Handshake,
        Plugins: map[string]plugin.Plugin{
            "connection": &connector.ConnectionNodePlugin{Impl: &MyConnector{}},
        },
    })
}
```

### Step 3: Build & Test

```bash
# Build the plugin binary
go build -o connector-example .

# Run locally (requires VRSky local dev environment)
vrsky plugin run ./connector-example --config test-config.yaml
```

---

## ⚡ Part 2: Building a Logic Plugin (TypeScript)

Logic plugins allow you to write business logic that runs safely inside the VRSky platform.

### Prerequisites
- Node.js 20+
- VRSky CLI (`npm install -g @vrsky/cli`)

### Step 1: Initialize

```bash
vrsky init logic my-converter --template typescript
cd my-converter
npm install
```

### Step 2: Write Logic

Edit `index.ts`:

```typescript
import { Message, Context, Converter } from "@vrsky/sdk";

// Convert incoming JSON to a simplified format
export const convert: Converter = (msg: Message, ctx: Context): Message => {
  const input = JSON.parse(msg.payload.toString());
  
  const output = {
    id: input.orderId,
    amount: input.total * 1.2, // Apply tax
    timestamp: new Date().toISOString()
  };

  return {
    ...msg,
    payload: Buffer.from(JSON.stringify(output)),
    contentType: "application/json"
  };
};
```

### Step 3: Build (Compile to WASM)

```bash
npm run build
# Output: dist/logic.wasm
```

### Step 4: Publish

```bash
vrsky plugin publish ./dist/logic.wasm --name "tax-calculator" --version "1.0.0"
```

---

## 🚀 Publishing to Marketplace

Once your plugin is tested:

1.  **Package**: Ensure you have a valid `manifest.json`.
2.  **Sign**: Sign your binary/WASM with your developer key.
3.  **Upload**: Submit to the VRSky Marketplace.
    ```bash
    vrsky marketplace submit .
    ```

Your plugin will undergo automated security scanning before being available to users.
