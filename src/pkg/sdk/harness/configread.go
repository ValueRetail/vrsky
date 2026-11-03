package harness

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// ConfigReadSpec describes how a producer reads its connection config, for
// RunConfigReadCases.
type ConfigReadSpec struct {
	// New builds the producer under test on db, as Configure would.
	New func(t *testing.T, db *sql.DB) sdk.Producer
	// Columns the producer's connection query scans, in order: "nodes", or
	// "nodes" and "edges".
	Columns []string
	// OwnNode is the config of a producer node of this connector's type. When
	// the producer resolves secrets it holds a "<field>_secret_id" reference;
	// leave OwnNodeHasSecret false for a producer that resolves none.
	OwnNode          string
	OwnNodeHasSecret bool
	// ForeignNode is the config of another connector's producer node with a
	// "<field>_secret_id" reference.
	ForeignNode string
}

// RunConfigReadCases checks the split #704 is about. A failed config or secret
// read must never ack: ServesConnection (when the producer implements it)
// answers true, and Deliver and DeliverStream (when implemented) return a
// retriable error marked sdk.ErrConfigUnavailable. A connection that is gone,
// or has no node of this producer's type, is "not mine" and acks as before,
// even when another connector's node has an unreadable secret.
func RunConfigReadCases(t *testing.T, spec ConfigReadSpec) {
	t.Helper()
	errDown := errors.New("pq: the database system is shutting down")

	cases := []struct {
		name      string
		skip      bool
		expect    func(m sqlmock.Sqlmock) // the reads of one config lookup
		wantAcked bool
	}{
		{
			name: "connection query fails",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`FROM connections`).WillReturnError(errDown)
			},
		},
		{
			name: "own node's secret read fails",
			skip: !spec.OwnNodeHasSecret,
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`FROM connections`).WillReturnRows(connectionRow(spec.Columns, spec.OwnNode))
				m.ExpectQuery(`FROM secrets`).WillReturnError(errDown)
			},
		},
		{
			name: "connection does not exist",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`FROM connections`).WillReturnRows(sqlmock.NewRows(spec.Columns))
			},
			wantAcked: true,
		},
		{
			name: "only another connector's node, its secret unreadable",
			expect: func(m sqlmock.Sqlmock) {
				// No secrets query is expected: an unexpected one fails
				// with a sqlmock error, which would read as unavailable.
				m.ExpectQuery(`FROM connections`).WillReturnRows(connectionRow(spec.Columns, spec.ForeignNode))
			},
			wantAcked: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip {
				t.Skip("producer resolves no secrets")
			}
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()
			p := spec.New(t, db)
			cs, scoped := p.(sdk.ConnectionScoped)
			sp, streams := p.(sdk.StreamingProducer)
			lookups := 1
			if scoped {
				lookups++
			}
			if streams {
				lookups++
			}
			for i := 0; i < lookups; i++ {
				tc.expect(mock)
			}

			ctx := context.Background()
			env := envelope.New()
			env.TenantID = "tenant-1"
			env.IntegrationID = "conn-1"
			env.Payload = []byte(`[{"id":1}]`)

			if scoped {
				if got := cs.ServesConnection(ctx, env.TenantID, env.IntegrationID); got == tc.wantAcked {
					t.Errorf("ServesConnection = %v, want %v", got, !tc.wantAcked)
				}
			}
			checkResult(t, "Deliver", p.Deliver(ctx, env), tc.wantAcked)
			if streams {
				checkResult(t, "DeliverStream", sp.DeliverStream(ctx, env, strings.NewReader(`[{"id":1}]`)), tc.wantAcked)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("config reads: %v", err)
			}
		})
	}
}

func checkResult(t *testing.T, what string, err error, wantAcked bool) {
	t.Helper()
	if wantAcked {
		if err != nil {
			t.Errorf("%s = %v, want nil (ack: not this producer's connection)", what, err)
		}
		return
	}
	switch {
	case err == nil:
		t.Errorf("%s = nil: the message would be acked and lost", what)
	case sdk.IsPermanent(err):
		t.Errorf("%s = Permanent(%v): the message would be acked and lost", what, err)
	case !sdk.IsConfigUnavailable(err):
		t.Errorf("%s = %v, want an error marked sdk.ErrConfigUnavailable", what, err)
	}
}

// connectionRow is a connections row holding one producer node with config.
func connectionRow(columns []string, config string) *sqlmock.Rows {
	nodes := `[{"id":"n1","type":"producer","config":` + config + `}]`
	values := make([]driver.Value, len(columns))
	for i, c := range columns {
		switch c {
		case "nodes":
			values[i] = []byte(nodes)
		case "edges":
			values[i] = []byte(`[]`)
		default:
			values[i] = ""
		}
	}
	return sqlmock.NewRows(columns).AddRow(values...)
}
