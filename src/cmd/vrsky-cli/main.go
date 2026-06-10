// vrsky-cli is the operator CLI for VRSky. Today it provides disaster-recovery
// tooling for the management Postgres (#86): take an encrypted backup to object
// storage, list backups, and restore one into a target database.
//
//	vrsky-cli backup
//	vrsky-cli list
//	vrsky-cli restore --target-db-url postgres://… --confirm <backup-key>
//
// Config comes from the environment (12-factor) — see backupConfigFromEnv.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

const usage = `vrsky-cli — VRSky operator CLI

Usage:
  vrsky-cli backup                 Dump the management DB, encrypt, upload to object storage
  vrsky-cli list                   List available backups (key + size + age)
  vrsky-cli restore [flags] <key>  Restore a backup into a target database

Run "vrsky-cli <command> -h" for command flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	ctx := context.Background()

	var err error
	switch cmd {
	case "backup":
		err = runBackup(ctx, args)
	case "list":
		err = runList(ctx, args)
	case "restore":
		err = runRestore(ctx, args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// backupConfig holds the resolved DR configuration from the environment.
type backupConfig struct {
	dbURL  string // management DB to dump (BACKUP_DB_URL, falls back to MGMT_API_DB_URL)
	keyHex string // ENCRYPTION_KEY (same master key as the secrets vault)
	prefix string // object key prefix (BACKUP_PREFIX, default "mgmt-backups/")
	store  *objectstore.Config
}

// backupConfigFromEnv assembles the DR config from env vars.
func backupConfigFromEnv() (*backupConfig, error) {
	dbURL := envOr("BACKUP_DB_URL", os.Getenv("MGMT_API_DB_URL"))
	if dbURL == "" {
		return nil, fmt.Errorf("set BACKUP_DB_URL (or MGMT_API_DB_URL) to the management database URL")
	}
	keyHex, err := crypto.Key()
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_KEY: %w", err)
	}
	bucket := os.Getenv("BACKUP_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("set BACKUP_BUCKET to the object-storage bucket for backups")
	}
	store := &objectstore.Config{
		Provider:        envOr("BACKUP_PROVIDER", objectstore.ProviderS3),
		Bucket:          bucket,
		Region:          os.Getenv("BACKUP_REGION"),
		Endpoint:        os.Getenv("BACKUP_ENDPOINT"),
		AccessKeyID:     os.Getenv("BACKUP_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("BACKUP_SECRET_ACCESS_KEY"),
		// Azure
		AccountName:      os.Getenv("BACKUP_AZURE_ACCOUNT_NAME"),
		AccountKey:       os.Getenv("BACKUP_AZURE_ACCOUNT_KEY"),
		ConnectionString: os.Getenv("BACKUP_AZURE_CONNECTION_STRING"),
		// GCS
		CredentialsJSON: os.Getenv("BACKUP_GCS_CREDENTIALS_JSON"),
	}
	return &backupConfig{
		dbURL:  dbURL,
		keyHex: keyHex,
		prefix: envOr("BACKUP_PREFIX", "mgmt-backups/"),
		store:  store,
	}, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
