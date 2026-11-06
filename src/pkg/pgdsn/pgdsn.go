// Package pgdsn builds lib/pq key=value connection strings from the parts a
// database node stores (host, port, user, password, database, sslmode).
//
// Every value is quoted, so a password with a space, a quote or a backslash
// reaches the server as typed. Built with a plain `key=%s` format instead, a
// password like `hunter2 secret` made lib/pq fail to parse the string with an
// error that quotes the part after the space (`missing "=" after "secret"`),
// and that error ends up in logs, the activity feed and the DLQ (#705).
package pgdsn

import (
	"strconv"
	"strings"
)

// Config is one database's connection settings.
type Config struct {
	Host     string
	Port     int // 0: 5432
	User     string
	Password string
	Database string
	SSLMode  string // "": disable
	// ConnectTimeout bounds connecting, TLS and authentication, in seconds.
	// 0 leaves it to the OS (minutes for a host that drops packets).
	ConnectTimeout int
}

// Build returns the connection string for c.
func Build(c Config) string {
	port := c.Port
	if port == 0 {
		port = 5432
	}
	ssl := c.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	parts := []string{
		"host=" + quote(c.Host),
		"port=" + strconv.Itoa(port),
		"user=" + quote(c.User),
		"password=" + quote(c.Password),
		"dbname=" + quote(c.Database),
		"sslmode=" + quote(ssl),
	}
	if c.ConnectTimeout > 0 {
		parts = append(parts, "connect_timeout="+strconv.Itoa(c.ConnectTimeout))
	}
	return strings.Join(parts, " ")
}

// quote wraps v in single quotes, escaping backslashes and quotes the way
// lib/pq's parser (and libpq's) expects.
func quote(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}
