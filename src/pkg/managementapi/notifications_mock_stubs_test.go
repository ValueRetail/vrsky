package managementapi

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MockRepository's notification-target methods carry real in-memory behaviour
// so notifications_handler_test.go can drive the handlers end-to-end. Same
// keyed-state pattern as oauth_mock_stubs_test.go.

type mockNotifState struct {
	mu      sync.Mutex
	targets map[string]*NotificationTarget
	secrets map[string]string // target ID -> plaintext secret
	nextID  int
}

var (
	notifStateMu sync.Mutex
	notifStates  = map[*MockRepository]*mockNotifState{}
)

func notifStateFor(m *MockRepository) *mockNotifState {
	notifStateMu.Lock()
	defer notifStateMu.Unlock()
	s, ok := notifStates[m]
	if !ok {
		s = &mockNotifState{
			targets: map[string]*NotificationTarget{},
			secrets: map[string]string{},
		}
		notifStates[m] = s
	}
	return s
}

func (m *MockRepository) CreateNotificationTarget(_ context.Context, t *NotificationTarget, secret string) error {
	s := notifStateFor(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Enforce UNIQUE (tenant_id, name) like Postgres does.
	for _, ex := range s.targets {
		if ex.TenantID == t.TenantID && ex.Name == t.Name {
			return ErrNotificationTargetNameExists
		}
	}
	s.nextID++
	t.ID = fmt.Sprintf("nt-%d", s.nextID)
	t.CreatedAt = time.Now()
	t.UpdatedAt = t.CreatedAt
	if secret != "" {
		t.SecretID = "sec-" + t.ID
		s.secrets[t.ID] = secret
	}
	cp := *t
	s.targets[t.ID] = &cp
	return nil
}

func (m *MockRepository) ListNotificationTargets(_ context.Context, tenantID string) ([]*NotificationTarget, error) {
	s := notifStateFor(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*NotificationTarget
	for _, t := range s.targets {
		if t.TenantID == tenantID {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MockRepository) GetNotificationTarget(_ context.Context, tenantID, id string) (*NotificationTarget, error) {
	s := notifStateFor(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[id]
	if !ok || t.TenantID != tenantID {
		return nil, ErrNotificationTargetNotFound
	}
	cp := *t
	return &cp, nil
}

func (m *MockRepository) UpdateNotificationTarget(_ context.Context, t *NotificationTarget, secret string) error {
	s := notifStateFor(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.targets[t.ID]
	if !ok || old.TenantID != t.TenantID {
		return ErrNotificationTargetNotFound
	}
	if secret != "" {
		t.SecretID = "sec-" + t.ID
		s.secrets[t.ID] = secret
	}
	t.UpdatedAt = time.Now()
	cp := *t
	s.targets[t.ID] = &cp
	return nil
}

func (m *MockRepository) DeleteNotificationTarget(_ context.Context, tenantID, id string) error {
	s := notifStateFor(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[id]
	if !ok || t.TenantID != tenantID {
		return ErrNotificationTargetNotFound
	}
	delete(s.targets, id)
	delete(s.secrets, id)
	return nil
}

func (m *MockRepository) ListNotificationTargetsForDispatch(_ context.Context, tenantID string) ([]*NotificationTarget, error) {
	s := notifStateFor(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*NotificationTarget
	for _, t := range s.targets {
		if !t.Enabled {
			continue
		}
		if tenantID != "" && t.TenantID == tenantID {
			cp := *t
			out = append(out, &cp)
		}
		if tenantID == "" && t.Config.Platform {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MockRepository) ResolveNotificationSecret(_ context.Context, t *NotificationTarget) (string, error) {
	s := notifStateFor(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secrets[t.ID], nil
}
