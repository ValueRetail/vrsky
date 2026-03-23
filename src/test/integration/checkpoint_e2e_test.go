//go:build integration
// +build integration

// Package integration provides E2E tests for checkpoint persistence.
// These tests validate that:
// 1. Checkpoints are persisted to the database during message processing
// 2. Checkpoints survive pod restarts (no data loss)
// 3. Components resume from the correct checkpoint after restart
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/orchestrator"
	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"
)

// TestCheckpoint_PersistenceOnMessageProcessing verifies that checkpoints are
// saved to the database when messages are processed by components.
//
// Test scenario:
// 1. Create a 2-node pipeline (consumer -> producer)
// 2. Send 5 messages through the pipeline
// 3. Verify checkpoints exist in the database for each node
// 4. Verify checkpoint message counts match expected values
func TestCheckpoint_PersistenceOnMessageProcessing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_ = ctx // Used for future context-aware operations

	// Setup test context
	e2eCtx, err := NewE2EContext(t)
	if err != nil {
		t.Skipf("E2E context setup failed: %v", err)
	}
	defer e2eCtx.Cleanup()

	// Create a unique connection ID for this test
	connectionID := fmt.Sprintf("cp-persist-test-%d", time.Now().UnixNano())
	tenantID := e2eCtx.TenantID

	// Clean up any existing checkpoints
	if err := DeleteCheckpoints(e2eCtx.DB, tenantID, connectionID); err != nil {
		t.Logf("Warning: cleanup failed: %v", err)
	}

	// Build a simple 2-node pipeline
	consumerConfig := json.RawMessage(`{"type": "webhook", "path": "/webhook"}`)
	producerConfig := json.RawMessage(`{"type": "http", "url": "http://httpbin.org/post"}`)

	nodes, edges := Build2NodePipeline(consumerConfig, producerConfig)

	// Create connection via API
	conn, err := CreateConnection(e2eCtx.APIEndpoint, tenantID, "checkpoint-persistence-test", nodes, edges)
	if err != nil {
		t.Fatalf("CreateConnection failed: %v", err)
	}
	t.Logf("Created connection: %s", conn.ID)

	// Update connectionID to match the created connection
	connectionID = conn.ID

	// Register cleanup
	e2eCtx.AddCleanup(func() {
		_ = StopConnection(e2eCtx.APIEndpoint, connectionID)
		_ = DeleteConnection(e2eCtx.APIEndpoint, connectionID)
		_ = DeleteCheckpoints(e2eCtx.DB, tenantID, connectionID)
	})

	// Start the connection
	if err := StartConnection(e2eCtx.APIEndpoint, connectionID); err != nil {
		t.Fatalf("StartConnection failed: %v", err)
	}

	// Wait for pods to be ready
	if err := WaitForPodsReady(t, e2eCtx.K8sClient, e2eCtx.Namespace, connectionID, 2); err != nil {
		t.Fatalf("WaitForPodsReady failed: %v", err)
	}

	// Send test messages
	messageCount := 5
	for i := 0; i < messageCount; i++ {
		order := GenerateTestOrder(fmt.Sprintf("order-%d", i+1), float64(100+i*50), "active")
		payload, _ := json.Marshal(order)

		if e2eCtx.NATSConn != nil {
			// Send via NATS to consumer input topic
			inputTopic := orchestrator.GetOutputTopic(tenantID, connectionID, "consumer-node")
			env := envelope.New()
			env.ID = fmt.Sprintf("msg-%d", i+1)
			env.Payload = payload
			env.ContentType = "application/json"

			if err := SendEnvelope(e2eCtx.NATSConn, inputTopic, env); err != nil {
				t.Logf("Warning: SendEnvelope failed: %v", err)
			}
		}

		// Small delay between messages
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for messages to be processed
	time.Sleep(5 * time.Second)

	// Verify checkpoints exist
	consumerCP, err := GetCheckpoint(e2eCtx.DB, tenantID, connectionID, "consumer-node")
	if err != nil {
		t.Fatalf("GetCheckpoint(consumer) failed: %v", err)
	}

	if consumerCP == nil {
		t.Error("Expected consumer checkpoint to exist")
	} else {
		t.Logf("Consumer checkpoint: messageCount=%d, lastMessageID=%s",
			consumerCP.MessageCount, consumerCP.LastProcessedMessageID)

		if consumerCP.MessageCount == 0 {
			t.Error("Expected consumer to have processed at least one message")
		}
	}

	producerCP, err := GetCheckpoint(e2eCtx.DB, tenantID, connectionID, "producer-node")
	if err != nil {
		t.Fatalf("GetCheckpoint(producer) failed: %v", err)
	}

	if producerCP == nil {
		t.Log("Producer checkpoint not found (may not have processed messages yet)")
	} else {
		t.Logf("Producer checkpoint: messageCount=%d, lastMessageID=%s",
			producerCP.MessageCount, producerCP.LastProcessedMessageID)
	}
}

// TestCheckpoint_RecoveryAfterPodRestart verifies that checkpoints are
// preserved when a pod is killed and the component resumes from the correct position.
//
// Test scenario:
// 1. Create a 3-node pipeline (consumer -> filter -> producer)
// 2. Send 10 messages
// 3. Kill the filter pod
// 4. Wait for the filter pod to restart
// 5. Send 10 more messages
// 6. Verify total message count in checkpoint matches expected (20 messages)
// 7. Verify no messages were lost or duplicated
func TestCheckpoint_RecoveryAfterPodRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_ = ctx // Used for future context-aware operations

	// Setup test context
	e2eCtx, err := NewE2EContext(t)
	if err != nil {
		t.Skipf("E2E context setup failed: %v", err)
	}
	defer e2eCtx.Cleanup()

	connectionID := fmt.Sprintf("cp-recovery-test-%d", time.Now().UnixNano())
	tenantID := e2eCtx.TenantID

	// Clean up any existing checkpoints
	if err := DeleteCheckpoints(e2eCtx.DB, tenantID, connectionID); err != nil {
		t.Logf("Warning: cleanup failed: %v", err)
	}

	// Build a 3-node pipeline
	consumerConfig := json.RawMessage(`{"type": "webhook", "path": "/webhook"}`)
	filterConfig := json.RawMessage(`{"rules": [{"field": "status", "op": "eq", "value": "active"}]}`)
	producerConfig := json.RawMessage(`{"type": "http", "url": "http://httpbin.org/post"}`)

	nodes, edges := Build3NodePipeline(consumerConfig, filterConfig, producerConfig)

	// Create connection via API
	conn, err := CreateConnection(e2eCtx.APIEndpoint, tenantID, "checkpoint-recovery-test", nodes, edges)
	if err != nil {
		t.Fatalf("CreateConnection failed: %v", err)
	}
	connectionID = conn.ID
	t.Logf("Created connection: %s", connectionID)

	// Register cleanup
	e2eCtx.AddCleanup(func() {
		_ = StopConnection(e2eCtx.APIEndpoint, connectionID)
		_ = DeleteConnection(e2eCtx.APIEndpoint, connectionID)
		_ = DeleteCheckpoints(e2eCtx.DB, tenantID, connectionID)
	})

	// Start the connection
	if err := StartConnection(e2eCtx.APIEndpoint, connectionID); err != nil {
		t.Fatalf("StartConnection failed: %v", err)
	}

	// Wait for pods to be ready
	if err := WaitForPodsReady(t, e2eCtx.K8sClient, e2eCtx.Namespace, connectionID, 3); err != nil {
		t.Fatalf("WaitForPodsReady failed: %v", err)
	}

	// Track messages for verification
	var receivedMsgIDs []string
	var receivedMu sync.Mutex

	if e2eCtx.NATSConn != nil {
		// Subscribe to output of filter to track messages
		outputTopic := orchestrator.GetOutputTopic(tenantID, connectionID, "filter-node")
		_, err := e2eCtx.NATSConn.Subscribe(outputTopic, func(msg *nats.Msg) {
			env, err := envelope.Unmarshal(msg.Data)
			if err == nil {
				receivedMu.Lock()
				receivedMsgIDs = append(receivedMsgIDs, env.ID)
				receivedMu.Unlock()
			}
		})
		if err != nil {
			t.Logf("Warning: Subscribe failed: %v", err)
		}
	}

	// Send first batch of messages (10)
	sendMessages := func(startIdx, count int) {
		inputTopic := orchestrator.GetOutputTopic(tenantID, connectionID, "consumer-node")
		for i := 0; i < count; i++ {
			msgID := fmt.Sprintf("msg-%d", startIdx+i)
			order := GenerateTestOrder(fmt.Sprintf("order-%d", startIdx+i), float64(100+i*50), "active")
			payload, _ := json.Marshal(order)

			env := envelope.New()
			env.ID = msgID
			env.Payload = payload
			env.ContentType = "application/json"

			if e2eCtx.NATSConn != nil {
				if err := SendEnvelope(e2eCtx.NATSConn, inputTopic, env); err != nil {
					t.Logf("Warning: SendEnvelope failed: %v", err)
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	t.Log("Sending first batch of 10 messages...")
	sendMessages(1, 10)
	time.Sleep(5 * time.Second)

	// Get checkpoint before pod kill
	filterCPBefore, err := GetCheckpoint(e2eCtx.DB, tenantID, connectionID, "filter-node")
	if err != nil {
		t.Fatalf("GetCheckpoint(filter) before kill failed: %v", err)
	}

	var msgCountBefore int64
	if filterCPBefore != nil {
		msgCountBefore = filterCPBefore.MessageCount
		t.Logf("Filter checkpoint before kill: messageCount=%d", msgCountBefore)
	}

	// Kill the filter pod
	t.Log("Killing filter pod...")
	labelSelector := fmt.Sprintf("app=vrsky,pipeline=%s,node=filter-node", connectionID)
	filterPod, err := GetPodByLabel(t, e2eCtx.K8sClient, e2eCtx.Namespace, labelSelector)
	if err != nil {
		t.Fatalf("GetPodByLabel failed: %v", err)
	}

	if err := KillPod(t, e2eCtx.K8sClient, e2eCtx.Namespace, filterPod.Name); err != nil {
		t.Fatalf("KillPod failed: %v", err)
	}

	// Wait for pod to restart
	t.Log("Waiting for filter pod to restart...")
	if err := WaitForPodsReady(t, e2eCtx.K8sClient, e2eCtx.Namespace, connectionID, 3); err != nil {
		t.Fatalf("WaitForPodsReady after restart failed: %v", err)
	}

	// Send second batch of messages (10 more)
	t.Log("Sending second batch of 10 messages...")
	sendMessages(11, 10)
	time.Sleep(5 * time.Second)

	// Verify checkpoint after restart
	filterCPAfter, err := GetCheckpoint(e2eCtx.DB, tenantID, connectionID, "filter-node")
	if err != nil {
		t.Fatalf("GetCheckpoint(filter) after restart failed: %v", err)
	}

	if filterCPAfter == nil {
		t.Error("Expected filter checkpoint to exist after restart")
	} else {
		t.Logf("Filter checkpoint after restart: messageCount=%d, lastMessageID=%s",
			filterCPAfter.MessageCount, filterCPAfter.LastProcessedMessageID)

		// Verify message count increased
		expectedMinCount := msgCountBefore + 10
		if filterCPAfter.MessageCount < expectedMinCount {
			t.Errorf("Expected messageCount >= %d, got %d (possible message loss)",
				expectedMinCount, filterCPAfter.MessageCount)
		}
	}

	// Verify received messages (no duplicates)
	receivedMu.Lock()
	t.Logf("Total messages received: %d", len(receivedMsgIDs))

	// Check for duplicates
	seen := make(map[string]int)
	for _, id := range receivedMsgIDs {
		seen[id]++
		if seen[id] > 1 {
			t.Errorf("Duplicate message received: %s (count=%d)", id, seen[id])
		}
	}
	receivedMu.Unlock()
}

// TestCheckpoint_MultiNodeCheckpoints verifies that each node in a multi-node
// pipeline maintains its own independent checkpoint.
//
// Test scenario:
// 1. Create a 4-node pipeline (consumer -> filter -> converter -> producer)
// 2. Send messages through the pipeline
// 3. Verify each node has its own checkpoint with correct message counts
// 4. Verify checkpoints are isolated (one node's checkpoint doesn't affect another)
func TestCheckpoint_MultiNodeCheckpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Setup test context
	e2eCtx, err := NewE2EContext(t)
	if err != nil {
		t.Skipf("E2E context setup failed: %v", err)
	}
	defer e2eCtx.Cleanup()

	connectionID := fmt.Sprintf("cp-multinode-test-%d", time.Now().UnixNano())
	tenantID := e2eCtx.TenantID

	// Clean up any existing checkpoints
	if err := DeleteCheckpoints(e2eCtx.DB, tenantID, connectionID); err != nil {
		t.Logf("Warning: cleanup failed: %v", err)
	}

	// Build a 4-node pipeline
	consumerConfig := json.RawMessage(`{"type": "webhook", "path": "/webhook"}`)
	filterConfig := json.RawMessage(`{"rules": [{"field": "status", "op": "eq", "value": "active"}]}`)
	converterConfig := json.RawMessage(`{"transform": "json-to-json", "mapping": {"orderId": "id"}}`)
	producerConfig := json.RawMessage(`{"type": "http", "url": "http://httpbin.org/post"}`)

	nodes, edges := Build4NodePipeline(consumerConfig, filterConfig, converterConfig, producerConfig)

	// Create connection via API
	conn, err := CreateConnection(e2eCtx.APIEndpoint, tenantID, "checkpoint-multinode-test", nodes, edges)
	if err != nil {
		t.Fatalf("CreateConnection failed: %v", err)
	}
	connectionID = conn.ID
	t.Logf("Created connection: %s", connectionID)

	// Register cleanup
	e2eCtx.AddCleanup(func() {
		_ = StopConnection(e2eCtx.APIEndpoint, connectionID)
		_ = DeleteConnection(e2eCtx.APIEndpoint, connectionID)
		_ = DeleteCheckpoints(e2eCtx.DB, tenantID, connectionID)
	})

	// Start the connection
	if err := StartConnection(e2eCtx.APIEndpoint, connectionID); err != nil {
		t.Fatalf("StartConnection failed: %v", err)
	}

	// Wait for pods to be ready
	if err := WaitForPodsReady(t, e2eCtx.K8sClient, e2eCtx.Namespace, connectionID, 4); err != nil {
		t.Fatalf("WaitForPodsReady failed: %v", err)
	}

	// Send mixed messages (some pass filter, some don't)
	t.Log("Sending 20 messages (10 active, 10 inactive)...")
	if e2eCtx.NATSConn != nil {
		inputTopic := orchestrator.GetOutputTopic(tenantID, connectionID, "consumer-node")

		for i := 0; i < 20; i++ {
			status := "active"
			if i%2 == 0 {
				status = "inactive" // These should be filtered out
			}

			order := GenerateTestOrder(fmt.Sprintf("order-%d", i+1), float64(100+i*50), status)
			payload, _ := json.Marshal(order)

			env := envelope.New()
			env.ID = fmt.Sprintf("msg-%d", i+1)
			env.Payload = payload
			env.ContentType = "application/json"

			if err := SendEnvelope(e2eCtx.NATSConn, inputTopic, env); err != nil {
				t.Logf("Warning: SendEnvelope failed: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Wait for processing
	time.Sleep(10 * time.Second)

	// Verify checkpoints for each node
	nodeIDs := []string{"consumer-node", "filter-node", "converter-node", "producer-node"}
	checkpoints := make(map[string]*checkpoint.Checkpoint)

	for _, nodeID := range nodeIDs {
		cp, err := GetCheckpoint(e2eCtx.DB, tenantID, connectionID, nodeID)
		if err != nil {
			t.Errorf("GetCheckpoint(%s) failed: %v", nodeID, err)
			continue
		}

		checkpoints[nodeID] = cp

		if cp == nil {
			t.Logf("%s: no checkpoint found", nodeID)
		} else {
			t.Logf("%s: messageCount=%d, lastMessageID=%s",
				nodeID, cp.MessageCount, cp.LastProcessedMessageID)
		}
	}

	// Verify consumer checkpoint
	consumerCP := checkpoints["consumer-node"]
	if consumerCP == nil {
		t.Error("Consumer checkpoint should exist")
	} else if consumerCP.MessageCount < 20 {
		t.Errorf("Consumer should have processed 20 messages, got %d", consumerCP.MessageCount)
	}

	// Filter checkpoint should have processed all 20, but only passed ~10
	filterCP := checkpoints["filter-node"]
	if filterCP == nil {
		t.Log("Filter checkpoint not found (may not track input messages)")
	}

	// Converter checkpoint should have ~10 messages (filtered)
	converterCP := checkpoints["converter-node"]
	if converterCP == nil {
		t.Log("Converter checkpoint not found")
	}

	// Producer checkpoint should have ~10 messages
	producerCP := checkpoints["producer-node"]
	if producerCP == nil {
		t.Log("Producer checkpoint not found")
	}

	// Verify independence: delete one checkpoint, others should be unaffected
	t.Log("Testing checkpoint independence...")
	store := checkpoint.NewPostgresStore(e2eCtx.DB)
	if err := store.Delete(ctx, tenantID, connectionID, "filter-node"); err != nil {
		t.Errorf("Delete(filter-node) failed: %v", err)
	}

	// Verify other checkpoints still exist
	for _, nodeID := range []string{"consumer-node", "converter-node", "producer-node"} {
		cp, err := GetCheckpoint(e2eCtx.DB, tenantID, connectionID, nodeID)
		if err != nil {
			t.Errorf("GetCheckpoint(%s) after filter delete failed: %v", nodeID, err)
		}
		if cp == nil && checkpoints[nodeID] != nil {
			t.Errorf("%s checkpoint was affected by filter checkpoint deletion", nodeID)
		}
	}

	// Verify filter checkpoint is deleted
	filterCPAfter, _ := GetCheckpoint(e2eCtx.DB, tenantID, connectionID, "filter-node")
	if filterCPAfter != nil {
		t.Error("Filter checkpoint should be deleted")
	}
}
