package orchestrator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func envByName(vars []corev1.EnvVar, name string) (corev1.EnvVar, bool) {
	for _, v := range vars {
		if v.Name == name {
			return v, true
		}
	}
	return corev1.EnvVar{}, false
}

func TestAppendPayloadStoreEnv_OffWhenNoBucket(t *testing.T) {
	base := []corev1.EnvVar{{Name: "X", Value: "1"}}
	got := appendPayloadStoreEnv(base, &OrchestratorConfig{}) // no bucket
	if len(got) != len(base) {
		t.Fatalf("expected no payload env when bucket unset, got %d extra", len(got)-len(base))
	}
	if _, ok := envByName(got, EnvPayloadStoreBucket); ok {
		t.Error("bucket env should be absent when feature is off")
	}
}

func TestAppendPayloadStoreEnv_NilConfig(t *testing.T) {
	base := []corev1.EnvVar{{Name: "X", Value: "1"}}
	if got := appendPayloadStoreEnv(base, nil); len(got) != len(base) {
		t.Fatalf("nil config should be a no-op")
	}
}

func TestAppendPayloadStoreEnv_PlainSettingsAndSecretRef(t *testing.T) {
	cfg := &OrchestratorConfig{
		PayloadStoreProvider:   "s3",
		PayloadStoreBucket:     "vrsky-objects",
		PayloadStoreEndpoint:   "http://minio.vrsky-storage.svc.cluster.local:9000",
		PayloadStoreRegion:     "us-east-1",
		PayloadStoreSecretName: "minio-credentials",
		PayloadInlineMaxBytes:  "262144",
	}
	got := appendPayloadStoreEnv(nil, cfg)

	// Plain settings.
	for name, want := range map[string]string{
		EnvPayloadStoreProvider:  "s3",
		EnvPayloadStoreBucket:    "vrsky-objects",
		EnvPayloadStoreEndpoint:  "http://minio.vrsky-storage.svc.cluster.local:9000",
		EnvPayloadStoreRegion:    "us-east-1",
		EnvPayloadInlineMaxBytes: "262144",
	} {
		v, ok := envByName(got, name)
		if !ok {
			t.Errorf("missing env %s", name)
			continue
		}
		if v.Value != want {
			t.Errorf("%s = %q, want %q", name, v.Value, want)
		}
		if v.ValueFrom != nil {
			t.Errorf("%s should be a plain value, not a ref", name)
		}
	}

	// Credentials must come from the secret, never as plaintext values.
	for name, key := range map[string]string{
		EnvPayloadStoreAccessKey: payloadSecretAccessKey,
		EnvPayloadStoreSecretKey: payloadSecretSecretKey,
	} {
		v, ok := envByName(got, name)
		if !ok {
			t.Fatalf("missing credential env %s", name)
		}
		if v.Value != "" {
			t.Errorf("%s must not be a plaintext value", name)
		}
		if v.ValueFrom == nil || v.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("%s must use a SecretKeyRef", name)
		}
		if v.ValueFrom.SecretKeyRef.Name != "minio-credentials" {
			t.Errorf("%s secret name = %q, want minio-credentials", name, v.ValueFrom.SecretKeyRef.Name)
		}
		if v.ValueFrom.SecretKeyRef.Key != key {
			t.Errorf("%s secret key = %q, want %q", name, v.ValueFrom.SecretKeyRef.Key, key)
		}
	}
}

func TestAppendPayloadStoreEnv_BucketOnlyOmitsOptional(t *testing.T) {
	// Only a bucket set (no secret name, no optional plain fields): bucket is
	// injected but nothing else — including no credential envs.
	got := appendPayloadStoreEnv(nil, &OrchestratorConfig{PayloadStoreBucket: "b"})
	if _, ok := envByName(got, EnvPayloadStoreBucket); !ok {
		t.Fatal("bucket env should be present")
	}
	for _, name := range []string{EnvPayloadStoreProvider, EnvPayloadStoreEndpoint, EnvPayloadStoreRegion, EnvPayloadInlineMaxBytes, EnvPayloadStoreAccessKey, EnvPayloadStoreSecretKey} {
		if _, ok := envByName(got, name); ok {
			t.Errorf("%s should be omitted when unset", name)
		}
	}
}
