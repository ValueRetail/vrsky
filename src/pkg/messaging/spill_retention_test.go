package messaging

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// lifecycleRules pulls the JSON lifecycle document out of the MinIO setup
// Job, where it is written through a shell heredoc inside the manifest.
func lifecycleRules(t *testing.T, manifest string) []struct {
	ID         string
	Filter     struct{ Prefix string }
	Expiration struct{ Days int }
} {
	t.Helper()
	m := regexp.MustCompile(`(?s)cat > /tmp/lifecycle\.json <<EOF\n(.*?)\n\s*EOF\n`).FindStringSubmatch(manifest)
	if m == nil {
		t.Fatal("setup-job.yaml no longer writes /tmp/lifecycle.json through a heredoc; update this test")
	}
	var doc struct {
		Rules []struct {
			ID         string
			Filter     struct{ Prefix string }
			Expiration struct{ Days int }
		}
	}
	if err := json.Unmarshal([]byte(m[1]), &doc); err != nil {
		t.Fatalf("lifecycle JSON in setup-job.yaml does not parse: %v\n%s", err, m[1])
	}
	return doc.Rules
}

// TestSpillLifecycleOutlivesRetention: an offloaded payload must stay in the
// bucket for as long as any message can still reference it — a delivery
// waiting for an offline remote agent (MainRetention) or a DLQ replay
// (DLQRetention). The rule lives in a manifest the Go code never reads, so
// this is the only thing that keeps the two in step: raising a retention
// here, or lowering the days there, fails this test.
func TestSpillLifecycleOutlivesRetention(t *testing.T) {
	path := filepath.Join("..", "..", "..", "infrastructure", "kubernetes", "minio", "setup-job.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("MinIO setup job not available (%v) — guard skipped", err)
	}

	var days int
	var found bool
	for _, r := range lifecycleRules(t, string(body)) {
		if strings.TrimSuffix(r.Filter.Prefix, "/") == "spill" {
			days, found = r.Expiration.Days, true
		}
	}
	if !found {
		t.Fatal("no lifecycle rule for the spill/ prefix — offloaded payloads would never be reclaimed")
	}

	ceilDays := func(d time.Duration) int { return int(math.Ceil(d.Hours() / 24)) }
	if want := ceilDays(SpillRetention); days < want {
		t.Errorf("spill/ lifecycle is %d days, but SpillRetention is %v (%d days): a DLQ replay would find its payload gone", days, SpillRetention, want)
	}
	if want := ceilDays(MainRetention); days < want {
		t.Errorf("spill/ lifecycle is %d days, but MainRetention is %v (%d days): an offline remote agent's large files would expire first", days, MainRetention, want)
	}
	if SpillRetention <= DLQRetention || SpillRetention <= MainRetention {
		t.Errorf("SpillRetention %v must exceed DLQRetention %v and MainRetention %v", SpillRetention, DLQRetention, MainRetention)
	}
}
