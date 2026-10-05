package managementapi

// The database/sql driver the management API runs on, registered as "pgx" —
// the same import as cmd/management-api/main.go. It is alone in this file so
// that changing the driver is a one-line diff and driver_integration_test.go
// can stay byte-for-byte the same across the change: that test is the proof
// the API behaves identically on the new driver.
import _ "github.com/jackc/pgx/v5/stdlib"
