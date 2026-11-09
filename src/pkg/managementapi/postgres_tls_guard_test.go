package managementapi

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Guards for TLS between the services and the in-cluster Postgres (#453).
//
// Nothing here talks to a cluster. Each test reads the files that turn TLS on
// and pins the links between them: the certificate the server mounts, the CA
// every client mounts, and the connection strings that name it. A broken link
// is a production outage (a client on verify-full with no CA file cannot
// connect), and nothing else in CI would notice.

const (
	pgCAMountDir  = "/etc/vrsky/postgres-ca"
	pgCAConfigMap = "postgres-ca"
	pgTLSSecret   = "postgresql-tls"
)

func k8sRoot() string { return filepath.Join("..", "..", "..", "infrastructure", "kubernetes") }

// readYAMLDocs parses every document of a YAML file into generic maps.
func readYAMLDocs(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("%s not available (%v) — guard skipped", path, err)
	}
	defer f.Close()
	var docs []map[string]any
	dec := yaml.NewDecoder(f)
	for {
		var d map[string]any
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
	return docs
}

// at walks nested maps and slices: at(v, "spec", "template", 0, "name").
func at(v any, keys ...any) any {
	for _, k := range keys {
		switch kk := k.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[kk]
		case int:
			s, ok := v.([]any)
			if !ok || kk >= len(s) {
				return nil
			}
			v = s[kk]
		}
	}
	return v
}

func str(v any) string { s, _ := v.(string); return s }

func list(v any) []any { l, _ := v.([]any); return l }

// podSpec returns the pod spec of a Deployment, StatefulSet or CronJob.
func podSpec(doc map[string]any) map[string]any {
	var v any
	switch str(doc["kind"]) {
	case "Deployment", "StatefulSet":
		v = at(doc, "spec", "template", "spec")
	case "CronJob":
		v = at(doc, "spec", "jobTemplate", "spec", "template", "spec")
	}
	m, _ := v.(map[string]any)
	return m
}

func docName(doc map[string]any) string {
	return str(doc["kind"]) + "/" + str(at(doc, "metadata", "name"))
}

func findDoc(t *testing.T, docs []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, d := range docs {
		if str(d["kind"]) == kind && str(at(d, "metadata", "name")) == name {
			return d
		}
	}
	t.Fatalf("no %s %q in the file — its shape changed; update this test", kind, name)
	return nil
}

// TestPostgresTLS_ServerMountsCertAndTurnsSSLOn: the StatefulSet has to start
// Postgres with ssl on, pointing at files that exist, owned the way Postgres
// demands.
func TestPostgresTLS_ServerMountsCertAndTurnsSSLOn(t *testing.T) {
	docs := readYAMLDocs(t, filepath.Join(k8sRoot(), "postgresql", "statefulset.yaml"))
	sts := findDoc(t, docs, "StatefulSet", "postgresql")
	spec := podSpec(sts)

	main, _ := at(spec, "containers", 0).(map[string]any)
	if str(main["name"]) != "postgresql" {
		t.Fatalf("first container is %q, want postgresql", str(main["name"]))
	}

	// -c settings: pairs of "-c" "name=value".
	args := list(main["args"])
	settings := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if str(args[i]) == "-c" {
			k, v, _ := strings.Cut(str(args[i+1]), "=")
			settings[k] = v
		}
	}
	if settings["ssl"] != "on" {
		t.Errorf("postgres is not started with -c ssl=on (args: %v)", args)
	}
	cert, key := settings["ssl_cert_file"], settings["ssl_key_file"]
	if cert == "" || key == "" {
		t.Fatalf("ssl_cert_file/ssl_key_file missing from the args: %v", args)
	}

	// Both files must sit under a mount of the container.
	var mounts []string
	for _, m := range list(main["volumeMounts"]) {
		mounts = append(mounts, str(at(m, "mountPath")))
	}
	for _, f := range []string{cert, key} {
		covered := false
		for _, m := range mounts {
			if mountCovers(m, f) {
				covered = true
			}
		}
		if !covered {
			t.Errorf("%s is not under any volumeMount of the postgresql container (%v)", f, mounts)
		}
	}

	// The key comes from the cert-manager Secret by way of an init container
	// that fixes owner and mode. Postgres refuses a key that others can read.
	volumes := map[string]map[string]any{}
	for _, v := range list(spec["volumes"]) {
		vm, _ := v.(map[string]any)
		volumes[str(vm["name"])] = vm
	}
	var srcVol string
	for name, v := range volumes {
		if str(at(v, "secret", "secretName")) == pgTLSSecret {
			srcVol = name
		}
	}
	if srcVol == "" {
		t.Fatalf("no volume from Secret %s in the StatefulSet (volumes: %v)", pgTLSSecret, volumes)
	}
	init, _ := at(spec, "initContainers", 0).(map[string]any)
	if init == nil {
		t.Fatal("no init container to fix the key's owner and mode")
	}
	script := strings.Join(strings.Fields(str(at(init, "command", 2))), " ")
	for _, want := range []string{"chmod 0600 /tls/tls.key", "chown 70:70 /tls/tls.crt /tls/tls.key"} {
		if !strings.Contains(script, want) {
			t.Errorf("init container script lacks %q: Postgres rejects a key it does not own or that others can read", want)
		}
	}
	// chmod must come before chown: after chown the container is no longer the
	// owner and (with only CAP_CHOWN) cannot chmod.
	if strings.Index(script, "chmod 0600") > strings.Index(script, "chown 70:70") {
		t.Error("init container chowns before it chmods; the chmod would fail without CAP_FOWNER")
	}
	// The copy Postgres reads lives in memory, not on a disk.
	copyVol := ""
	for _, m := range list(main["volumeMounts"]) {
		if mountCovers(str(at(m, "mountPath")), cert) {
			copyVol = str(at(m, "name"))
		}
	}
	if medium := str(at(volumes[copyVol], "emptyDir", "medium")); medium != "Memory" {
		t.Errorf("volume %q holding the private key has medium %q, want Memory", copyVol, medium)
	}

	// fsGroup would chmod the data directory group-writable, which Postgres
	// refuses to start on. The init container exists so this is not needed.
	if sc, ok := spec["securityContext"].(map[string]any); ok {
		if _, has := sc["fsGroup"]; has {
			t.Error("the pod sets fsGroup: kubelet would make PGDATA group-writable and Postgres refuses to start")
		}
	}
}

// TestPostgresTLS_CertificateCoversTheServiceName: a connection string says
// postgresql.vrsky-database.svc.cluster.local; verify-full checks that name
// against the certificate. The Secret the StatefulSet mounts has to be the one
// the Certificate writes, signed by the CA Issuer, which in turn signs with
// the CA Certificate's Secret.
func TestPostgresTLS_CertificateCoversTheServiceName(t *testing.T) {
	tls := readYAMLDocs(t, filepath.Join(k8sRoot(), "postgresql", "tls.yaml"))
	svcDocs := readYAMLDocs(t, filepath.Join(k8sRoot(), "postgresql", "service.yaml"))
	svc := findDoc(t, svcDocs, "Service", "postgresql")
	ns := str(at(svc, "metadata", "namespace"))

	leaf := findDoc(t, tls, "Certificate", "postgresql-tls")
	if got := str(at(leaf, "spec", "secretName")); got != pgTLSSecret {
		t.Errorf("Certificate postgresql-tls writes Secret %q, the StatefulSet mounts %q", got, pgTLSSecret)
	}
	have := map[string]bool{}
	for _, n := range list(at(leaf, "spec", "dnsNames")) {
		have[str(n)] = true
	}
	for _, want := range []string{
		str(at(svc, "metadata", "name")) + "." + ns + ".svc.cluster.local",
		str(at(svc, "metadata", "name")) + "." + ns + ".svc",
	} {
		if !have[want] {
			t.Errorf("Certificate postgresql-tls does not list %s; verify-full would reject the Service name", want)
		}
	}

	// Chain: leaf -> Issuer postgresql-ca -> Secret postgresql-ca <- CA Certificate.
	issuerName := str(at(leaf, "spec", "issuerRef", "name"))
	issuer := findDoc(t, tls, "Issuer", issuerName)
	caSecret := str(at(issuer, "spec", "ca", "secretName"))
	if caSecret == "" {
		t.Fatalf("Issuer %s is not a CA issuer", issuerName)
	}
	caCert := findDoc(t, tls, "Certificate", "postgresql-ca")
	if str(at(caCert, "spec", "secretName")) != caSecret {
		t.Errorf("CA Issuer signs with Secret %q but the CA Certificate writes %q", caSecret, str(at(caCert, "spec", "secretName")))
	}
	if at(caCert, "spec", "isCA") != true {
		t.Error("the CA Certificate is not isCA: true")
	}
	for _, d := range tls {
		if got := str(at(d, "metadata", "namespace")); got != ns {
			t.Errorf("%s is in namespace %q, the Service is in %q", docName(d), got, ns)
		}
	}
}

type pgClient struct {
	file string
	name string
}

// TestPostgresTLS_EveryClientMountsTheCA: every workload in the manifests that
// reads the platform connection string must carry the CA, or its next restart
// on verify-full cannot connect. Found by what they read, not by a list, so a
// connector added later without the mount fails here.
func TestPostgresTLS_EveryClientMountsTheCA(t *testing.T) {
	root := k8sRoot()
	if _, err := os.Stat(root); err != nil {
		t.Skipf("manifests not available (%v)", err)
	}
	var clients []pgClient
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".yaml" {
			return err
		}
		if strings.Contains(p, string(filepath.Separator)+"postgresql"+string(filepath.Separator)) ||
			strings.Contains(p, "secret") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		dec := yaml.NewDecoder(f)
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil // not plain YAML (a Helm values file); not a manifest we guard
			}
			spec := podSpec(doc)
			if spec == nil {
				continue
			}
			if !readsPostgresURL(spec) {
				continue
			}
			clients = append(clients, pgClient{p, docName(doc)})
			if msg := missingCA(spec); msg != "" {
				t.Errorf("%s (%s): %s", docName(doc), p, msg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 3 core services, the backup CronJob and the 31 connector Deployments.
	if len(clients) < 35 {
		t.Fatalf("found only %d workloads that read the Postgres URL (%v); the manifests changed shape, update this test", len(clients), clients)
	}
}

// readsPostgresURL: the pod takes DATABASE_URL / MGMT_API_DB_URL from the
// postgres-credentials Secret, or BACKUP_DB_URL from vrsky-backup-config.
func readsPostgresURL(spec map[string]any) bool {
	for _, c := range list(spec["containers"]) {
		for _, e := range list(at(c, "env")) {
			if str(at(e, "valueFrom", "secretKeyRef", "name")) == "postgres-credentials" &&
				str(at(e, "valueFrom", "secretKeyRef", "key")) == "connection_string" {
				return true
			}
		}
		for _, e := range list(at(c, "envFrom")) {
			if str(at(e, "secretRef", "name")) == "vrsky-backup-config" {
				return true
			}
		}
	}
	return false
}

func missingCA(spec map[string]any) string {
	found := false
	for _, v := range list(spec["volumes"]) {
		if str(at(v, "configMap", "name")) == pgCAConfigMap {
			found = true
			// optional, so a pod starts before the ConfigMap exists (and the
			// rollout order does not matter).
			if at(v, "configMap", "optional") != true {
				return "the " + pgCAConfigMap + " volume is not optional: pods would not start before ca-sync has run"
			}
		}
	}
	if !found {
		return "no volume from ConfigMap " + pgCAConfigMap
	}
	for _, c := range list(spec["containers"]) {
		for _, m := range list(at(c, "volumeMounts")) {
			if str(at(m, "mountPath")) == pgCAMountDir {
				return ""
			}
		}
	}
	return "no container mounts the CA at " + pgCAMountDir
}

// TestPostgresTLS_ProductionStringsUseVerifyFull: the connection string
// rotate-secrets.sh builds is what production runs on.
func TestPostgresTLS_ProductionStringsUseVerifyFull(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "infrastructure", "azure", "rotate-secrets.sh"))
	if err != nil {
		t.Skipf("rotate-secrets.sh not available (%v)", err)
	}
	src := string(raw)
	conn := regexp.MustCompile(`(?m)^CONN="([^"]*)"`).FindStringSubmatch(src)
	if conn == nil {
		t.Fatal("no CONN=\"…\" line in rotate-secrets.sh — update this test")
	}
	if strings.Contains(conn[1], "sslmode=disable") {
		t.Error("rotate-secrets.sh builds the production connection string with sslmode=disable")
	}
	if !strings.Contains(conn[1], "sslmode=verify-full") {
		t.Errorf("CONN %q does not say sslmode=verify-full", conn[1])
	}
	if !strings.Contains(conn[1], "sslrootcert=${PG_CA_PATH}") {
		t.Errorf("CONN %q does not name the CA file", conn[1])
	}
	if !strings.Contains(src, "PG_CA_PATH="+pgCAMountDir+"/ca.crt") {
		t.Errorf("PG_CA_PATH is not %s/ca.crt, where every client pod mounts the CA", pgCAMountDir)
	}
	// verify-full against a server without TLS cannot connect: refuse to rotate.
	if !strings.Contains(src, "SHOW ssl") {
		t.Error("rotate-secrets.sh does not check that Postgres has ssl on before it writes verify-full strings")
	}
	// The service the strings name has to be one the certificate covers.
	if !strings.Contains(conn[1], "@${PG_HOST}:") || !strings.Contains(src, "PG_HOST=postgresql.vrsky-database.svc.cluster.local") {
		t.Error("the connection string host is not postgresql.vrsky-database.svc.cluster.local, a name the certificate covers")
	}
}

// TestPostgresTLS_NoClientSaysSSLModeDisable: production manifests and
// scripts must not turn TLS off, and since #729 Postgres refuses a client
// that does, the k3d dev examples included (they say sslmode=require).
// postgres-tls.sh names the word because `urls disable` exists, guarded.
func TestPostgresTLS_NoClientSaysSSLModeDisable(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	exceptions := map[string]string{
		"infrastructure/azure/postgres-tls.sh":               "`urls disable`, refused while Postgres is hostssl",
		"infrastructure/kubernetes/postgresql/README.md":     "describes the rollback",
		"infrastructure/kubernetes/management-api/README.md": "a local test database for MGMT_TEST_DB_URL, not a deployed client",
	}
	targets := []string{"infrastructure/kubernetes", "infrastructure/azure", "docs/operator/backup.md", "docs/operator/encryption-at-rest.md"}
	var scanned int
	for _, target := range targets {
		base := filepath.Join(root, filepath.FromSlash(target))
		if _, err := os.Stat(base); err != nil {
			t.Skipf("%s not available (%v)", target, err)
		}
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if _, ok := exceptions[rel]; ok {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			scanned++
			isDoc := strings.HasSuffix(rel, ".md")
			for i, line := range strings.Split(string(b), "\n") {
				// A comment is not a client. Prose in docs is, because docs
				// hold the commands people paste.
				if !isDoc && strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				if strings.Contains(line, "sslmode=disable") {
					t.Errorf("%s:%d says sslmode=disable; production clients use verify-full (#453). "+
						"If this is a dev-only file, add it to the exceptions with a reason.", rel, i+1)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d files; the layout changed, update this test", scanned)
	}
}

// bashRetarget runs retarget_url / url_sslmode from postgres-tls.sh.
func bashRetarget(t *testing.T, fn string, args ...string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "infrastructure", "azure", "postgres-tls.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("postgres-tls.sh not available (%v)", err)
	}
	cmd := exec.Command(bash, append([]string{"-c", `. "$1"; shift; fn="$1"; shift; "$fn" "$@"`, "_", script, fn}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", fn, args, err, out)
	}
	return string(out)
}

// TestPostgresTLS_ScriptRewritesOnlyTheSSLParameters: `urls` changes sslmode
// inside a Secret it must never print, so the rewrite is pinned here: it
// replaces sslmode and sslrootcert, keeps every other parameter, and a round
// trip back to disable restores what the Secret said before.
func TestPostgresTLS_ScriptRewritesOnlyTheSSLParameters(t *testing.T) {
	const base = "postgresql://vrsky:pw@postgresql.vrsky-database.svc.cluster.local:5432/vrsky"
	ca := pgCAMountDir + "/ca.crt"
	cases := []struct {
		name, in, mode, want string
	}{
		{"disable to verify-full", base + "?sslmode=disable", "verify-full", base + "?sslmode=verify-full&sslrootcert=" + ca},
		{"no query", base, "verify-full", base + "?sslmode=verify-full&sslrootcert=" + ca},
		{"keeps other parameters", base + "?application_name=a&sslmode=require&connect_timeout=5", "verify-full",
			base + "?application_name=a&connect_timeout=5&sslmode=verify-full&sslrootcert=" + ca},
		{"already verify-full is stable", base + "?sslmode=verify-full&sslrootcert=" + ca, "verify-full", base + "?sslmode=verify-full&sslrootcert=" + ca},
		{"rollback drops the CA", base + "?sslmode=verify-full&sslrootcert=" + ca, "disable", base + "?sslmode=disable"},
		// With a root certificate libpq and lib/pq treat require as verify-ca,
		// so the TLS-without-checking rollback must drop it.
		{"require drops the CA", base + "?application_name=a&sslmode=verify-full&sslrootcert=" + ca, "require", base + "?application_name=a&sslmode=require"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bashRetarget(t, "retarget_url", c.in, c.mode); got != c.want {
				t.Errorf("retarget_url(%q, %s)\n got  %q\n want %q", c.in, c.mode, got, c.want)
			}
		})
	}
	if got := bashRetarget(t, "url_sslmode", base+"?sslmode=verify-full&sslrootcert="+ca); got != "verify-full" {
		t.Errorf("url_sslmode = %q, want verify-full", got)
	}
	if got := bashRetarget(t, "url_sslmode", base); got != "none" {
		t.Errorf("url_sslmode of a string with no sslmode = %q, want none", got)
	}
}

// TestPostgresTLS_ScriptNeverPrintsAConnectionString: the script handles the
// password inside the URL. It may read it into a variable and pipe it on, and
// that is all.
func TestPostgresTLS_ScriptNeverPrintsAConnectionString(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "infrastructure", "azure", "postgres-tls.sh"))
	if err != nil {
		t.Skipf("postgres-tls.sh not available (%v)", err)
	}
	src := string(raw)
	if regexp.MustCompile(`(?m)^\s*set\s+-[a-z]*x`).MatchString(src) {
		t.Error("postgres-tls.sh turns on xtrace; every command, with its connection string, would be printed")
	}
	// A variable holding a URL must not reach echo or a message.
	// url_sslmode "$cur" prints only the mode, and is the one safe use.
	safe := regexp.MustCompile(`url_sslmode\s+"\$[a-z]+"`)
	leak := regexp.MustCompile(`^\s*(echo|say|die)\b[^\n]*\$\{?(cur|new|url)\b`)
	for _, line := range strings.Split(src, "\n") {
		if leak.MatchString(safe.ReplaceAllString(line, "")) {
			t.Errorf("postgres-tls.sh prints a variable that holds a connection string: %s", strings.TrimSpace(line))
		}
	}
	// The patch goes in on stdin, never in argv where `ps` shows it.
	if !strings.Contains(src, "--patch-file /dev/stdin") {
		t.Error("postgres-tls.sh no longer writes the Secret patch through stdin")
	}
	if regexp.MustCompile(`kubectl\s+patch\s+secret[^\n]*\s-p\s`).MatchString(src) {
		t.Error("postgres-tls.sh patches a Secret with -p, which puts the value on the command line")
	}
}

// pgHBAConfigMap is the ConfigMap statefulset.yaml carries the server's
// pg_hba.conf in (#729).
const pgHBAConfigMap = "postgres-hba"

// TestPostgresTLS_RefusesPlainConnections: Postgres reads pg_hba.conf from a
// ConfigMap in the manifest (hba_file), not from the data volume, so a
// snapshot restore or a new volume refuses plain connections too (#729). The
// file has to keep the pod's own way in (the Unix socket the probes, the
// image's first-start server and `kubectl exec psql` use) and make every
// other address use TLS.
func TestPostgresTLS_RefusesPlainConnections(t *testing.T) {
	docs := readYAMLDocs(t, filepath.Join(k8sRoot(), "postgresql", "statefulset.yaml"))
	sts := findDoc(t, docs, "StatefulSet", "postgresql")
	cm := findDoc(t, docs, "ConfigMap", pgHBAConfigMap)

	// One `kubectl apply -f statefulset.yaml` must create the ConfigMap before
	// the pod that cannot start without it.
	cmAt, stsAt := -1, -1
	for i, d := range docs {
		switch docName(d) {
		case "ConfigMap/" + pgHBAConfigMap:
			cmAt = i
		case "StatefulSet/postgresql":
			stsAt = i
		}
	}
	if cmAt > stsAt {
		t.Errorf("ConfigMap %s comes after the StatefulSet in statefulset.yaml; apply would start the pod before its pg_hba.conf exists", pgHBAConfigMap)
	}
	if a, b := str(at(cm, "metadata", "namespace")), str(at(sts, "metadata", "namespace")); a != b {
		t.Errorf("ConfigMap %s is in namespace %q, the StatefulSet in %q", pgHBAConfigMap, a, b)
	}

	spec := podSpec(sts)
	main, _ := at(spec, "containers", 0).(map[string]any)
	args := list(main["args"])
	hbaFile := ""
	for i := 0; i+1 < len(args); i++ {
		if str(args[i]) == "-c" {
			if k, v, _ := strings.Cut(str(args[i+1]), "="); k == "hba_file" {
				hbaFile = v
			}
		}
	}
	if hbaFile == "" {
		t.Fatalf("postgres is not started with -c hba_file=…; it would read $PGDATA/pg_hba.conf from the volume (args: %v)", args)
	}

	// The file has to come from that ConfigMap, through a directory mount: a
	// subPath file never changes after the pod starts, and the documented
	// emergency rollback edits the ConfigMap and reloads.
	key := ""
	for _, m := range list(main["volumeMounts"]) {
		mp := str(at(m, "mountPath"))
		if !mountCovers(mp, hbaFile) {
			continue
		}
		if at(m, "subPath") != nil {
			t.Errorf("the mount at %s uses subPath; an edit of the ConfigMap would never reach the pod", mp)
		}
		var vol map[string]any
		for _, v := range list(spec["volumes"]) {
			if vm, _ := v.(map[string]any); str(vm["name"]) == str(at(m, "name")) {
				vol = vm
			}
		}
		if got := str(at(vol, "configMap", "name")); got != pgHBAConfigMap {
			t.Fatalf("%s is mounted from %v, want ConfigMap %s", hbaFile, vol, pgHBAConfigMap)
		}
		if at(vol, "configMap", "optional") == true {
			t.Errorf("the %s volume is optional; without the file Postgres does not start, so the pod should wait for it", pgHBAConfigMap)
		}
		key = strings.TrimPrefix(strings.TrimPrefix(hbaFile, strings.TrimSuffix(mp, "/")), "/")
		for _, it := range list(at(vol, "configMap", "items")) {
			if str(at(it, "path")) == key {
				key = str(at(it, "key"))
			}
		}
	}
	if key == "" {
		t.Fatalf("%s is not under any volumeMount of the postgresql container", hbaFile)
	}
	hba := str(at(cm, "data", key))
	if hba == "" {
		t.Fatalf("ConfigMap %s has no data key %q, the file hba_file names", pgHBAConfigMap, key)
	}
	for _, p := range hbaProblems(hba) {
		t.Error(p)
	}

	// The probes must reach Postgres the way the local line lets them: over
	// the socket, so no -h.
	for _, probe := range []string{"livenessProbe", "readinessProbe"} {
		cmd := strings.Join(strings.Fields(strings.Join(func() []string {
			var out []string
			for _, c := range list(at(main, probe, "exec", "command")) {
				out = append(out, str(c))
			}
			return out
		}(), " ")), " ")
		if !strings.Contains(cmd, "pg_isready") {
			t.Errorf("%s is %q, not pg_isready", probe, cmd)
		}
		if strings.Contains(cmd, " -h") || strings.Contains(cmd, "--host") {
			t.Errorf("%s connects over TCP (%q); pg_hba.conf only trusts the socket and loopback", probe, cmd)
		}
	}
}

// hbaProblems: what is wrong with a pg_hba.conf for this server. Only the pod
// itself (socket, loopback) may connect without TLS or without a password.
func hbaProblems(hba string) []string {
	var probs []string
	loopback := map[string]bool{"127.0.0.1/32": true, "::1/128": true}
	localAll, remoteTLS := false, false
	for n, line := range strings.Split(hba, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		where := fmt.Sprintf("pg_hba.conf line %d (%q)", n+1, strings.TrimSpace(line))
		switch f[0] {
		case "local":
			if len(f) < 4 {
				probs = append(probs, where+": too few fields")
				continue
			}
			if f[1] == "all" && f[2] == "all" {
				localAll = true
			}
		case "host", "hostssl", "hostnossl", "hostgssenc":
			if len(f) < 5 {
				probs = append(probs, where+": too few fields (an address with a separate netmask is not used here)")
				continue
			}
			addr, method := f[3], f[4]
			if loopback[addr] {
				continue
			}
			if f[0] != "hostssl" {
				probs = append(probs, where+": a client from another pod may connect without TLS; use hostssl")
			}
			if method == "trust" {
				probs = append(probs, where+": trust for a non-loopback address lets any pod in without a password")
			}
			if f[0] == "hostssl" && f[1] == "all" && f[2] == "all" && addr == "all" && method == "scram-sha-256" {
				remoteTLS = true
			}
		default:
			probs = append(probs, where+": record type "+f[0]+"; this guard knows local and host* lines only (an include would hide rules from it)")
		}
	}
	if !localAll {
		probs = append(probs, "no `local all all` line: the probes, the image's first-start server and kubectl exec psql use the socket and would be refused")
	}
	if !remoteTLS {
		probs = append(probs, "no `hostssl all all all scram-sha-256` line: no client from another pod could connect")
	}
	return probs
}

// TestPostgresTLS_HBACheckCatchesPlainRules: hbaProblems itself, on files it
// must reject and one it must accept.
func TestPostgresTLS_HBACheckCatchesPlainRules(t *testing.T) {
	good := "local all all trust\nhost all all 127.0.0.1/32 trust\nhostssl all all all scram-sha-256\n"
	if p := hbaProblems(good); len(p) > 0 {
		t.Errorf("rejected a good file: %v", p)
	}
	for name, hba := range map[string]string{
		"host for all addresses":   "local all all trust\nhost all all all scram-sha-256\n",
		"hostnossl beside hostssl": good + "hostnossl all all 10.0.0.0/8 scram-sha-256\n",
		"trust over TLS":           "local all all trust\nhostssl all all all trust\n",
		"no local line":            "hostssl all all all scram-sha-256\n",
		"an include":               good + "include_dir conf.d\n",
	} {
		if len(hbaProblems(hba)) == 0 {
			t.Errorf("%s: accepted\n%s", name, hba)
		}
	}
}
