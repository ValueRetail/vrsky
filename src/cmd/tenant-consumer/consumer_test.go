package main

import (
	"context"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// tenant-consumer implements the SDK Consumer contract.
var _ sdk.Consumer = (*tenantConsumer)(nil)

// Configure must refuse to start without a management DB (per-bridge config and
// the tenant_data_connections permission check both live there).
func TestTenantConsumer_ConfigureRequiresDB(t *testing.T) {
	c := &tenantConsumer{}
	// DB is nil here; Configure returns before it touches Health.
	if err := c.Configure(context.Background(), &sdk.Resources{}); err == nil {
		t.Fatal("expected Configure to fail without a DB")
	}
}
