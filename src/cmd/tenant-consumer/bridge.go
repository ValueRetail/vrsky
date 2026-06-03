package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/fieldfilter"
	// lint:connector-ok — the cross-tenant bridge owns its own durable
	// subscription on the *source* tenant's stream, which the SDK's single
	// command-driven subscription model does not cover. Importing pkg/messaging
	// directly here is deliberate; see runBridge below.
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// dataConnectionInfo holds the permission info from tenant_data_connections
type dataConnectionInfo struct {
	AllowedFields       []string
	DeniedFields        []string
	SharedConnectionIDs []string
}

// runBridge subscribes to the source tenant's NATS topic and republishes to the target tenant's pipeline
func (s *tenantConsumer) runBridge(ctx context.Context, connectionID, tenantID string, config *TenantConsumerConfig) {
	logger := s.logger.With(
		"connection_id", connectionID,
		"tenant_id", tenantID,
		"source_tenant_id", config.SourceTenantID,
	)

	// Look up the data connection to verify permission and get field filters
	dcInfo, err := s.getDataConnectionInfo(tenantID, config.SourceTenantID)
	if err != nil {
		logger.Error("No approved data connection found", "error", err)
		_ = s.updateConnectionStatus(connectionID, tenantID, "error")
		s.mu.Lock()
		delete(s.activeBridges, connectionID)
		s.mu.Unlock()
		return
	}

	// Verify the source connection is in the shared list (if shared list is non-empty)
	if len(dcInfo.SharedConnectionIDs) > 0 && config.SourceConnectionID != "" {
		allowed := false
		for _, id := range dcInfo.SharedConnectionIDs {
			if id == config.SourceConnectionID {
				allowed = true
				break
			}
		}
		if !allowed {
			logger.Error("Source connection not in shared list",
				"source_connection_id", config.SourceConnectionID,
				"shared_ids", dcInfo.SharedConnectionIDs)
			_ = s.updateConnectionStatus(connectionID, tenantID, "error")
			s.mu.Lock()
			delete(s.activeBridges, connectionID)
			s.mu.Unlock()
			return
		}
	}

	// Determine NATS subscription topic
	var topic string
	if config.SourceConnectionID != "" {
		topic = fmt.Sprintf("vrsky.data.%s.pipeline.%s", config.SourceTenantID, config.SourceConnectionID)
	} else if len(dcInfo.SharedConnectionIDs) == 1 {
		// Only one shared connection, subscribe specifically
		topic = fmt.Sprintf("vrsky.data.%s.pipeline.%s", config.SourceTenantID, dcInfo.SharedConnectionIDs[0])
	} else {
		// Subscribe to all pipelines from this source tenant
		topic = fmt.Sprintf("vrsky.data.%s.pipeline.>", config.SourceTenantID)
	}

	logger.Info("Subscribing to source via JetStream", "filter_subject", topic)

	js, jsErr := s.nc.JetStream()
	if jsErr != nil {
		logger.Error("JetStream context", "error", jsErr)
		_ = s.updateConnectionStatus(connectionID, tenantID, "error")
		s.mu.Lock()
		delete(s.activeBridges, connectionID)
		s.mu.Unlock()
		return
	}
	// Each bridge gets its own durable consumer with a FilterSubject set
	// to the specific source pipeline(s) — durable name encodes the
	// requester's connection so two bridges to the same source maintain
	// independent ack state.
	sub, err := messaging.Subscribe(js, messaging.SubscriberOpts{
		DurableName:   "tenant-bridge-" + connectionID,
		FilterSubject: topic,
		Logger:        logger,
	}, func(ctx context.Context, msg *nats.Msg) error {
		s.handleSourceMessage(ctx, msg, connectionID, tenantID, dcInfo, logger)
		return nil
	})
	if err != nil {
		logger.Error("Failed to subscribe to source topic", "error", err, "topic", topic)
		_ = s.updateConnectionStatus(connectionID, tenantID, "error")
		s.mu.Lock()
		delete(s.activeBridges, connectionID)
		s.mu.Unlock()
		return
	}

	logger.Info("Bridge active, listening for data")

	if config.SourceConnectionID != "" {
		s.replayOrTrigger(ctx, config.SourceTenantID, config.SourceConnectionID, connectionID, tenantID, dcInfo, logger)
	} else if len(dcInfo.SharedConnectionIDs) > 0 {
		for _, scID := range dcInfo.SharedConnectionIDs {
			s.replayOrTrigger(ctx, config.SourceTenantID, scID, connectionID, tenantID, dcInfo, logger)
		}
	}

	<-ctx.Done()
	sub.Stop()
	logger.Info("Bridge stopped")
}

// handleSourceMessage processes a message from the source tenant and republishes it
func (s *tenantConsumer) handleSourceMessage(ctx context.Context, msg *nats.Msg, targetConnectionID, targetTenantID string, dcInfo *dataConnectionInfo, logger *slog.Logger) {
	// Unmarshal the envelope
	var env envelope.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		logger.Error("Failed to unmarshal envelope", "error", err)
		return
	}

	logger.Debug("Received envelope from source",
		"envelope_id", env.ID,
		"source_tenant", env.TenantID,
		"payload_size", len(env.Payload))

	// Apply field filtering if the payload is JSON
	filteredPayload := env.Payload
	if len(dcInfo.AllowedFields) > 0 || len(dcInfo.DeniedFields) > 0 {
		filteredPayload = fieldfilter.FilterFields(json.RawMessage(env.Payload), dcInfo.AllowedFields, dcInfo.DeniedFields)
	}

	// Create new envelope for the target tenant's pipeline
	newEnv := &envelope.Envelope{
		ID:            uuid.New().String(),
		TenantID:      targetTenantID,
		IntegrationID: targetConnectionID,
		Payload:       filteredPayload,
		PayloadSize:   int64(len(filteredPayload)),
		ContentType:   env.ContentType,
		Source:        fmt.Sprintf("tenant-consumer:%s", env.TenantID),
		CurrentStep:   0,
		StepHistory:   []string{"tenant-consumer"},
		Metadata:      env.Metadata,
		CreatedAt:     time.Now().UTC(),
	}

	// Marshal once for the last_payload cache below; the SDK publish path
	// marshals the envelope itself (envelope.Marshal == json.Marshal, so the
	// bytes are identical).
	data, err := json.Marshal(newEnv)
	if err != nil {
		logger.Error("Failed to marshal new envelope", "error", err)
		return
	}

	// Publish to the target tenant's pipeline stream via the SDK's injected
	// publish func (the one data-emit path).
	if err := s.publish(ctx, newEnv); err != nil {
		logger.Error("Failed to publish to target tenant via JetStream", "error", err,
			"target_tenant", targetTenantID, "target_connection", targetConnectionID)
		return
	}

	// Store last_payload for the target connection (used by filter data structure preview)
	_, _ = s.db.Exec("UPDATE connections SET last_payload = $1 WHERE id = $2", data, targetConnectionID)

	logger.Info("Bridged envelope",
		"source_envelope_id", env.ID,
		"new_envelope_id", newEnv.ID,
		"target_tenant", targetTenantID,
		"target_connection", targetConnectionID,
		"payload_size", len(filteredPayload))
}

// replayOrTrigger tries to replay cached data from the source connection.
// If no cache is available, falls back to triggering the source pipeline.
func (s *tenantConsumer) replayOrTrigger(ctx context.Context, sourceTenantID, sourceConnectionID, targetConnectionID, targetTenantID string, dcInfo *dataConnectionInfo, logger *slog.Logger) {
	var lastPayload []byte

	// Try exact connection ID first
	if sourceConnectionID != "" {
		_ = s.db.QueryRow("SELECT last_payload FROM connections WHERE id = $1 AND last_payload IS NOT NULL", sourceConnectionID).Scan(&lastPayload)
	}

	// Fallback: find any connection from the source tenant that has cached data
	if lastPayload == nil {
		_ = s.db.QueryRow("SELECT last_payload FROM connections WHERE tenant_id = $1 AND last_payload IS NOT NULL ORDER BY updated_at DESC LIMIT 1", sourceTenantID).Scan(&lastPayload)
	}

	if lastPayload != nil {
		msg := &nats.Msg{Data: lastPayload}
		s.handleSourceMessage(ctx, msg, targetConnectionID, targetTenantID, dcInfo, logger)
		logger.Info("Replayed cached data from source", "source_connection_id", sourceConnectionID)
		return
	}

	// No cache available — fall back to triggering source pipeline
	logger.Info("No cached data, triggering source pipeline", "source_connection_id", sourceConnectionID)
	s.triggerSourcePipeline(sourceTenantID, sourceConnectionID, logger)
}

// triggerSourcePipeline sends a start command to re-run a source pipeline
func (s *tenantConsumer) triggerSourcePipeline(sourceTenantID, sourceConnectionID string, logger *slog.Logger) {
	cmd := struct {
		ConnectionID string `json:"connection_id"`
		TenantID     string `json:"tenant_id"`
	}{
		ConnectionID: sourceConnectionID,
		TenantID:     sourceTenantID,
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		logger.Error("Failed to marshal trigger command", "error", err)
		return
	}
	topic := fmt.Sprintf("vrsky.commands.%s.connection.start", sourceTenantID)
	if err := s.nc.Publish(topic, data); err != nil {
		logger.Error("Failed to trigger source pipeline", "error", err, "topic", topic)
		return
	}
	logger.Info("Triggered source pipeline re-run", "source_connection_id", sourceConnectionID, "topic", topic)
}

// getDataConnectionInfo looks up an active data connection between two tenants
func (s *tenantConsumer) getDataConnectionInfo(requesterTenantID, targetTenantID string) (*dataConnectionInfo, error) {
	var allowedJSON, deniedJSON, sharedJSON []byte
	err := s.db.QueryRow(`
		SELECT allowed_fields, denied_fields, shared_connection_ids
		FROM tenant_data_connections
		WHERE (
			(requester_tenant_id = $1 AND target_tenant_id = $2)
			OR (requester_tenant_id = $2 AND target_tenant_id = $1)
		) AND status = 'active'
		LIMIT 1
	`, requesterTenantID, targetTenantID).Scan(&allowedJSON, &deniedJSON, &sharedJSON)
	if err != nil {
		return nil, fmt.Errorf("no active data connection: %w", err)
	}

	info := &dataConnectionInfo{}
	if allowedJSON != nil {
		_ = json.Unmarshal(allowedJSON, &info.AllowedFields)
	}
	if deniedJSON != nil {
		_ = json.Unmarshal(deniedJSON, &info.DeniedFields)
	}
	if sharedJSON != nil {
		_ = json.Unmarshal(sharedJSON, &info.SharedConnectionIDs)
	}
	return info, nil
}
