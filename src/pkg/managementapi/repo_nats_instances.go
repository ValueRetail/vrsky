package managementapi

import (
	"context"
	"errors"
	"time"
)

// Tenant NATS instance tracking + service discovery (#21). The nats_instances
// table (init-schema.sql) records every per-tenant NATS instance the control
// plane provisions; workers discover their tenant's instances through the
// /api/v1/tenants/{id}/nats-instances API rather than a hardcoded URL, and the
// autoscaler (#19) updates capacity metrics here.

// ErrNATSInstanceNotFound is returned when no matching instance row exists.
var ErrNATSInstanceNotFound = errors.New("nats instance not found")

// NATSInstance mirrors one row of nats_instances.
type NATSInstance struct {
	ID               string     `json:"id"`
	TenantID         string     `json:"tenant_id"`
	InstanceNumber   int        `json:"instance_number"`
	DNSName          string     `json:"dns_name"`
	Status           string     `json:"status"` // provisioning | active | unhealthy | decommissioned
	IntegrationCount int        `json:"integration_count"`
	MessageRateAvg   int64      `json:"message_rate_avg"`
	ConnectionCount  int        `json:"connection_count"`
	MemoryUsageMB    int        `json:"memory_usage_mb"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	DeletedAt        *time.Time `json:"deleted_at,omitempty"`
}

// NATSURL returns the client URL for this instance (NATS listens on 4222).
func (n *NATSInstance) NATSURL() string {
	return "nats://" + n.DNSName + ":4222"
}

// NATSInstanceStore is the narrow persistence surface the discovery API,
// provisioner wiring, health loop, and autoscaler need. Satisfied by
// *PostgresRepository; obtained via a type assertion on h.repo so the broad
// Repository interface (and its mocks) stays untouched.
type NATSInstanceStore interface {
	ListNATSInstances(ctx context.Context, tenantID string) ([]*NATSInstance, error)
	ListActiveNATSInstancesAllTenants(ctx context.Context) ([]*NATSInstance, error)
	RegisterNATSInstance(ctx context.Context, tenantID string, instanceNumber int, dnsName string) (*NATSInstance, error)
	SetNATSInstanceStatus(ctx context.Context, id, status string) error
	SoftDeleteNATSInstance(ctx context.Context, tenantID, id string) error
	UpdateNATSInstanceMetrics(ctx context.Context, id string, integrations, connections, memoryMB int, msgRate int64) error
}

const natsInstanceCols = `id, tenant_id, instance_number, dns_name, status,
	integration_count, message_rate_avg, connection_count, memory_usage_mb,
	created_at, updated_at, deleted_at`

func scanNATSInstance(s interface {
	Scan(dest ...any) error
}) (*NATSInstance, error) {
	n := &NATSInstance{}
	err := s.Scan(&n.ID, &n.TenantID, &n.InstanceNumber, &n.DNSName, &n.Status,
		&n.IntegrationCount, &n.MessageRateAvg, &n.ConnectionCount, &n.MemoryUsageMB,
		&n.CreatedAt, &n.UpdatedAt, &n.DeletedAt)
	return n, err
}

// ListNATSInstances returns a tenant's live (active) instances, ordered by
// instance number — the set workers connect to.
func (r *PostgresRepository) ListNATSInstances(ctx context.Context, tenantID string) ([]*NATSInstance, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+natsInstanceCols+`
		FROM nats_instances
		WHERE tenant_id = $1 AND status = 'active' AND deleted_at IS NULL
		ORDER BY instance_number`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NATSInstance
	for rows.Next() {
		n, err := scanNATSInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListActiveNATSInstancesAllTenants returns every active/unhealthy instance
// across all tenants — used by the control-plane health loop (#21) and the
// autoscaler (#19), which operate platform-wide rather than per tenant.
func (r *PostgresRepository) ListActiveNATSInstancesAllTenants(ctx context.Context) ([]*NATSInstance, error) {
	// lint:tenant-ok — platform-wide control loop; intentionally cross-tenant.
	rows, err := r.db.QueryContext(ctx, `SELECT `+natsInstanceCols+`
		FROM nats_instances
		WHERE status IN ('active','unhealthy') AND deleted_at IS NULL
		ORDER BY tenant_id, instance_number`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NATSInstance
	for rows.Next() {
		n, err := scanNATSInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// RegisterNATSInstance inserts a new instance row in 'provisioning' state. The
// provisioner flips it to 'active' once the K8s resources are ready.
func (r *PostgresRepository) RegisterNATSInstance(ctx context.Context, tenantID string, instanceNumber int, dnsName string) (*NATSInstance, error) {
	// lint:tenant-ok — INSERT carries tenant_id in the row.
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO nats_instances (tenant_id, instance_number, dns_name, status)
		VALUES ($1, $2, $3, 'provisioning')
		RETURNING `+natsInstanceCols, tenantID, instanceNumber, dnsName)
	return scanNATSInstance(row)
}

// SetNATSInstanceStatus updates an instance's status by id. Used by the
// provisioner (→active on success) and the health loop (→unhealthy/active).
// Keyed by the instance PK, which the caller has already resolved.
func (r *PostgresRepository) SetNATSInstanceStatus(ctx context.Context, id, status string) error {
	// lint:tenant-ok — keyed by instance PK; status transitions are driven by
	// platform control loops that don't carry tenant context.
	res, err := r.db.ExecContext(ctx, `
		UPDATE nats_instances SET status = $2, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`, id, status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNATSInstanceNotFound
	}
	return nil
}

// SoftDeleteNATSInstance marks an instance decommissioned (tenant-scoped).
func (r *PostgresRepository) SoftDeleteNATSInstance(ctx context.Context, tenantID, id string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE nats_instances SET status = 'decommissioned', deleted_at = NOW(), updated_at = NOW()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNATSInstanceNotFound
	}
	return nil
}

// UpdateNATSInstanceMetrics records the latest capacity snapshot for an instance
// (#19 autoscaler monitor). Keyed by PK.
func (r *PostgresRepository) UpdateNATSInstanceMetrics(ctx context.Context, id string, integrations, connections, memoryMB int, msgRate int64) error {
	// lint:tenant-ok — keyed by instance PK; metrics scrape is platform-wide.
	_, err := r.db.ExecContext(ctx, `
		UPDATE nats_instances
		   SET integration_count = $2, connection_count = $3, memory_usage_mb = $4,
		       message_rate_avg = $5, updated_at = NOW()
		 WHERE id = $1 AND deleted_at IS NULL`, id, integrations, connections, memoryMB, msgRate)
	return err
}

// compile-time guard.
var _ NATSInstanceStore = (*PostgresRepository)(nil)
