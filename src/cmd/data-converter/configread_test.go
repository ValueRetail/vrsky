package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/configread"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// A failed read of the connection must NAK the message, not ack it (#704). A
// connection that is gone, or has no converter node, still acks.
func TestConverter_ConfigReadFailureIsNotAcked(t *testing.T) {
	cases := []struct {
		name      string
		expect    func(m sqlmock.Sqlmock)
		wantAcked bool
	}{
		{
			name: "connection query fails",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`FROM connections`).WillReturnError(errors.New("pq: the database system is shutting down"))
			},
		},
		{
			name: "connection does not exist",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`FROM connections`).WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}))
			},
			wantAcked: true,
		},
		{
			name: "no converter node",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`FROM connections`).WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).
					AddRow([]byte(`[{"id":"n1","type":"producer","config":{"type":"http"}}]`), []byte(`[]`)))
			},
			wantAcked: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()
			tc.expect(mock)

			s := newTestConverterService(nil)
			s.db = db
			s.pipelineCache = map[string]*ConverterPipelineInfo{}
			s.pipelineCacheTime = map[string]time.Time{}

			env := envelope.New()
			env.TenantID = "tenant-x"
			env.IntegrationID = "conn-1"
			env.ContentType = "application/json"
			env.Payload = []byte(`[{"id":1}]`)
			data, _ := json.Marshal(env)
			err = s.handleMessage(context.Background(), &nats.Msg{Subject: "vrsky.data.tenant-x.pipeline.conn-1", Data: data})

			if tc.wantAcked && err != nil {
				t.Errorf("handleMessage = %v, want nil (ack)", err)
			}
			if !tc.wantAcked && !configread.IsUnavailable(err) {
				t.Errorf("handleMessage = %v, want an error marked configread.ErrUnavailable (NAK)", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("config reads: %v", err)
			}
		})
	}
}
