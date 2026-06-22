// lint-tenant-filter — Phase 1I (#74) static check.
//
// Walks the management-api package looking for SQL statements that touch
// a tenant-scoped table without filtering on tenant_id. Designed as a
// belt-and-braces complement to the runtime ownership checks: even if a
// reviewer misses a new query, the CI build fails.
//
// What counts as "tenant-scoped"? Tables that have a `tenant_id` column
// in the schema. The list below is hard-coded — keep it in sync with
// new migrations.
//
// What's exempt?
//   - INSERT statements (a fresh row carries its own tenant_id in the
//     VALUES clause; no WHERE needed).
//   - Migrations and one-off scripts under cmd/migrate-secrets/.
//   - Queries already wrapped in a transaction inside an explicit
//     `-- lint:tenant-ok` comment (escape hatch for the rare legitimate
//     case, e.g. an admin job that operates across tenants).
//
// Usage:
//
//	go run ./cmd/lint-tenant-filter
//
// Exits non-zero on any violation. Wire into Makefile / CI.
package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// tenantScopedTables are the tables that MUST be filtered by tenant_id
// in any read/update/delete. Sourced from infrastructure/migrations/.
var tenantScopedTables = []string{
	"connections",
	"connection_events",
	"secrets",
	"audit_log",
	"oidc_config",
	"tenant_quotas",
	"user_tenant_roles",
	"tenant_data_connections",
	"tenant_connection_requests",
	"tenant_data_access_log",
	"tenant_api_keys",
	"oauth_providers",
	"oauth_grants",
	"notification_targets",
	"usage_daily",
}

// sqlStmt extracts the backtick-quoted SQL string that follows a
// QueryContext / QueryRowContext / ExecContext call.
var sqlStmt = regexp.MustCompile("(?s)(?:QueryRowContext|QueryContext|ExecContext)\\s*\\([^,]+,\\s*`([^`]*)`")

// suppressMarker is the per-call escape hatch.
const suppressMarker = "lint:tenant-ok"

func main() {
	root := os.Getenv("LINT_ROOT")
	if root == "" {
		root = "pkg/managementapi"
	}

	var violations []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		v, err := checkFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lint: failed to read %s: %v\n", path, err)
			os.Exit(2)
		}
		violations = append(violations, v...)
		return nil
	})

	if len(violations) > 0 {
		fmt.Fprintln(os.Stderr, "tenant-filter lint failed:")
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, "  -", v)
		}
		os.Exit(1)
	}
	fmt.Println("tenant-filter lint: ok")
}

func checkFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Read the whole file because SQL strings span lines.
	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		sb.WriteString(scanner.Text())
		sb.WriteByte('\n')
	}
	content := sb.String()

	var out []string
	for _, m := range sqlStmt.FindAllStringSubmatchIndex(content, -1) {
		start, end := m[2], m[3]
		stmt := content[start:end]
		ll := strings.ToLower(stmt)

		// Skip INSERTs and CREATEs.
		trim := strings.TrimSpace(ll)
		if strings.HasPrefix(trim, "insert") || strings.HasPrefix(trim, "create") {
			continue
		}
		// Skip explicit opt-outs. We look at the line CONTAINING the
		// call AND the three preceding lines — that covers inline
		// markers, marker-on-line-above, and the common pattern of
		// marker-above-a-var-declaration that precedes the query.
		preStart := m[0]
		preLineStart := preStart
		newlines := 0
		for preLineStart > 0 && newlines < 4 {
			preLineStart--
			if content[preLineStart] == '\n' {
				newlines++
			}
		}
		if strings.Contains(content[preLineStart:m[1]], suppressMarker) {
			continue
		}

		// Does it touch a tenant-scoped table?
		touched := ""
		for _, tbl := range tenantScopedTables {
			if regexp.MustCompile("\\b" + regexp.QuoteMeta(tbl) + "\\b").MatchString(ll) {
				touched = tbl
				break
			}
		}
		if touched == "" {
			continue
		}

		// Does the SQL filter by tenant_id? Accept WHERE tenant_id, AND
		// tenant_id, JOIN ... ON ... .tenant_id forms.
		if regexp.MustCompile(`(?i)tenant_id\s*=`).MatchString(stmt) {
			continue
		}

		// Find the call-site line number for a useful error.
		line := 1 + strings.Count(content[:m[0]], "\n")
		out = append(out, fmt.Sprintf("%s:%d touches %q without tenant_id filter — add a WHERE clause or // %s", path, line, touched, suppressMarker))
	}
	return out, nil
}
