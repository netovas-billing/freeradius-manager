package manager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Migration is one ordered SQL file that DBManager.ImportSchema applies.
// Mirrors internal/schema.Migration so we don't create an import cycle.
type Migration struct {
	Name string
	SQL  string
}

// DBManager owns the connection to the local MariaDB and provides the
// CRUD operations needed by instance lifecycle. The connection is
// expected to authenticate as a user with sufficient privileges to
// CREATE DATABASE / CREATE USER / GRANT (typically root@localhost via
// unix socket on a fresh install).
type DBManager struct {
	DB *sql.DB

	// AllowRemote mirrors radius-manager.sh's ALLOW_REMOTE_DB.
	// When true, an additional user@RemoteHost is created.
	AllowRemote bool
	RemoteHost  string // default "%"
}

// nameRegexp validates an identifier used in CREATE DATABASE / CREATE USER.
// We restrict to a strict whitelist instead of trying to escape, because
// MariaDB identifier escaping is non-trivial and easy to get wrong.
var nameRegexp = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

func validateIdentifier(s string) error {
	if !nameRegexp.MatchString(s) {
		return fmt.Errorf("%w: %q", ErrInvalidName, s)
	}
	return nil
}

// CreateDatabase is idempotent: skips if the database already exists.
// Equivalent to bash's create_database first half.
func (m *DBManager) CreateDatabase(ctx context.Context, name string) error {
	if err := validateIdentifier(name); err != nil {
		return err
	}
	// Probe via USE — bash uses the same trick.
	if _, err := m.DB.ExecContext(ctx, "USE `"+name+"`"); err == nil {
		return nil
	}
	stmt := fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", name)
	if _, err := m.DB.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	return nil
}

// CreateUserAndGrant ensures a MariaDB user exists with the given password
// and grants SELECT/INSERT/UPDATE/DELETE on the named database. Idempotent
// in the sense that it ALTERs the password if the user already exists.
//
// When AllowRemote is true the operation is also performed for user@RemoteHost.
func (m *DBManager) CreateUserAndGrant(ctx context.Context, dbName, user, password string) error {
	if err := validateIdentifier(dbName); err != nil {
		return err
	}
	if err := validateIdentifier(user); err != nil {
		return err
	}
	// Password may contain anything that MariaDB allows; we never inject
	// it into a backtick context, only single-quoted strings, and we
	// reject embedded single-quotes here.
	if strings.ContainsAny(password, `'\;`) {
		return fmt.Errorf("password contains forbidden characters")
	}

	hosts := []string{"localhost"}
	if m.AllowRemote {
		hosts = append(hosts, m.remoteHost())
	}

	for _, host := range hosts {
		exists, err := m.userExists(ctx, user, host)
		if err != nil {
			return err
		}
		var stmt string
		if exists {
			stmt = fmt.Sprintf("ALTER USER '%s'@'%s' IDENTIFIED BY '%s'", user, host, password)
		} else {
			stmt = fmt.Sprintf("CREATE USER '%s'@'%s' IDENTIFIED BY '%s'", user, host, password)
		}
		if _, err := m.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("user %s@%s: %w", user, host, err)
		}
		grant := fmt.Sprintf("GRANT SELECT,INSERT,UPDATE,DELETE ON `%s`.* TO '%s'@'%s'", dbName, user, host)
		if _, err := m.DB.ExecContext(ctx, grant); err != nil {
			return fmt.Errorf("grant %s@%s: %w", user, host, err)
		}
	}

	if _, err := m.DB.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

func (m *DBManager) userExists(ctx context.Context, user, host string) (bool, error) {
	q := fmt.Sprintf(
		"SELECT COUNT(*) FROM mysql.user WHERE user='%s' AND host='%s'",
		user, host,
	)
	row := m.DB.QueryRowContext(ctx, q)
	var n int
	if err := row.Scan(&n); err != nil {
		return false, fmt.Errorf("query user existence: %w", err)
	}
	return n > 0, nil
}

// DropDatabase removes the database and any users associated with it.
// Equivalent to bash's drop_database. Idempotent.
func (m *DBManager) DropDatabase(ctx context.Context, dbName, user string) error {
	if err := validateIdentifier(dbName); err != nil {
		return err
	}
	if err := validateIdentifier(user); err != nil {
		return err
	}

	// Drop DB if exists.
	if _, err := m.DB.ExecContext(ctx, "USE `"+dbName+"`"); err == nil {
		if _, err := m.DB.ExecContext(ctx, "DROP DATABASE `"+dbName+"`"); err != nil {
			return fmt.Errorf("drop database %s: %w", dbName, err)
		}
	}

	hosts := []string{"localhost"}
	if m.AllowRemote {
		hosts = append(hosts, m.remoteHost())
	}
	for _, host := range hosts {
		exists, err := m.userExists(ctx, user, host)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		stmt := fmt.Sprintf("DROP USER '%s'@'%s'", user, host)
		if _, err := m.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("drop user %s@%s: %w", user, host, err)
		}
	}

	if _, err := m.DB.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush privileges: %w", err)
	}
	return nil
}

// ImportSchema applies migrations to the named database in order.
// Skipped (no-op) if the radcheck table already exists — matches bash
// import_schema's existence probe.
func (m *DBManager) ImportSchema(ctx context.Context, dbName string, migrations []Migration) error {
	if err := validateIdentifier(dbName); err != nil {
		return err
	}

	// Probe radcheck — bash uses SHOW TABLES LIKE.
	probe := fmt.Sprintf("SHOW TABLES FROM `%s` LIKE 'radcheck'", dbName)
	rows, err := m.DB.QueryContext(ctx, probe)
	if err != nil {
		return fmt.Errorf("probe schema: %w", err)
	}
	hasRadcheck := rows.Next()
	rows.Close()
	if hasRadcheck {
		return nil
	}

	if _, err := m.DB.ExecContext(ctx, "USE `"+dbName+"`"); err != nil {
		return fmt.Errorf("use %s: %w", dbName, err)
	}

	for _, mig := range migrations {
		if _, err := m.DB.ExecContext(ctx, mig.SQL); err != nil {
			return fmt.Errorf("apply migration %s: %w", mig.Name, err)
		}
	}
	return nil
}

func (m *DBManager) remoteHost() string {
	if m.RemoteHost == "" {
		return "%"
	}
	return m.RemoteHost
}

// Sentinel for tests / callers.
var ErrDBClosed = errors.New("database connection is closed")
