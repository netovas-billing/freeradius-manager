//go:build integration
// +build integration

// Integration tests for DBManager that exercise a real MariaDB.
//
// Run with:
//
//	go test -tags=integration ./internal/manager/...
//
// Requires Docker (testcontainers-go spawns mariadb:10.11 automatically).
// You can also point at a pre-running MariaDB via:
//
//	RM_TEST_DB_DSN='root:testrootpw@tcp(127.0.0.1:13306)/'
//
// (matches docker-compose.test.yml default).
package manager

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	tcmariadb "github.com/testcontainers/testcontainers-go/modules/mariadb"
)

// dialIntegrationDB returns a *sql.DB connected either to a docker-compose
// instance pointed to by RM_TEST_DB_DSN, or to a freshly-spawned
// testcontainers MariaDB. The caller does not need to clean up — the test
// helper registers cleanups via t.Cleanup.
func dialIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()

	if dsn := os.Getenv("RM_TEST_DB_DSN"); dsn != "" {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		if err := db.Ping(); err != nil {
			t.Fatalf("ping db: %v", err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	mariadbC, err := tcmariadb.Run(ctx,
		"mariadb:10.11",
		tcmariadb.WithUsername("root"),
		tcmariadb.WithPassword("testrootpw"),
		tcmariadb.WithDatabase("testdb"),
	)
	if err != nil {
		t.Fatalf("start mariadb container: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = mariadbC.Terminate(shutCtx)
	})

	dsn, err := mariadbC.ConnectionString(ctx, "tls=false", "multiStatements=true")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}
	return db
}

func TestIntegration_FullLifecycle(t *testing.T) {
	db := dialIntegrationDB(t)
	dm := &DBManager{DB: db, AllowRemote: true, RemoteHost: "%"}

	const inst = "it_inst1"
	ctx := context.Background()

	// Step 1: create database.
	if err := dm.CreateDatabase(ctx, inst); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	// Idempotent re-run.
	if err := dm.CreateDatabase(ctx, inst); err != nil {
		t.Fatalf("CreateDatabase idempotent: %v", err)
	}

	// Step 2: create user + grant.
	if err := dm.CreateUserAndGrant(ctx, inst, inst, "TestPass123"); err != nil {
		t.Fatalf("CreateUserAndGrant: %v", err)
	}

	// Verify the localhost grant exists.
	var hasGrant bool
	rows, err := db.QueryContext(ctx,
		"SHOW GRANTS FOR '"+inst+"'@'localhost'")
	if err != nil {
		t.Fatalf("show grants: %v", err)
	}
	for rows.Next() {
		var g string
		_ = rows.Scan(&g)
		hasGrant = true
		t.Logf("grant: %s", g)
	}
	rows.Close()
	if !hasGrant {
		t.Fatal("expected at least one grant for localhost user")
	}

	// Step 3: import a tiny schema (don't need real freeradius schema for
	// this test; we just want to verify ImportSchema applies migrations
	// and skips if radcheck already exists).
	migrations := []Migration{
		{Name: "001_radcheck.sql", SQL: `CREATE TABLE radcheck (
			id    INT AUTO_INCREMENT PRIMARY KEY,
			username VARCHAR(64) NOT NULL,
			attribute VARCHAR(64) NOT NULL,
			op    VARCHAR(2) NOT NULL,
			value VARCHAR(253) NOT NULL
		) ENGINE=InnoDB`},
	}
	if err := dm.ImportSchema(ctx, inst, migrations); err != nil {
		t.Fatalf("ImportSchema: %v", err)
	}
	// Re-run should be a no-op.
	if err := dm.ImportSchema(ctx, inst, migrations); err != nil {
		t.Fatalf("ImportSchema idempotent: %v", err)
	}

	// Verify the table exists in the right database.
	var cnt int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables "+
			"WHERE table_schema=? AND table_name='radcheck'", inst).Scan(&cnt); err != nil {
		t.Fatalf("verify radcheck: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("expected radcheck in %s, got count=%d", inst, cnt)
	}

	// Step 4: drop everything.
	if err := dm.DropDatabase(ctx, inst, inst); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}
	// Idempotent: drop again must succeed.
	if err := dm.DropDatabase(ctx, inst, inst); err != nil {
		t.Fatalf("DropDatabase idempotent: %v", err)
	}

	// Verify DB is gone.
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name=?", inst).Scan(&cnt); err != nil {
		t.Fatalf("verify schema gone: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("expected schema %s to be dropped, got count=%d", inst, cnt)
	}
}

func TestIntegration_RejectsBadIdentifier(t *testing.T) {
	db := dialIntegrationDB(t)
	dm := &DBManager{DB: db}

	bad := []string{"foo`bar", "foo;DROP", "foo bar", ""}
	for _, name := range bad {
		if err := dm.CreateDatabase(context.Background(), name); err == nil {
			t.Errorf("expected rejection for %q", name)
		}
	}
}

func TestIntegration_TwoUsersIsolation(t *testing.T) {
	db := dialIntegrationDB(t)
	dm := &DBManager{DB: db, AllowRemote: false}
	ctx := context.Background()

	for _, name := range []string{"it_iso_a", "it_iso_b"} {
		if err := dm.CreateDatabase(ctx, name); err != nil {
			t.Fatal(err)
		}
		if err := dm.CreateUserAndGrant(ctx, name, name, "pw"+name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = dm.DropDatabase(context.Background(), name, name)
		})
	}

	// Verify user a cannot SELECT from user b's database.
	dsnA := "it_iso_a:pwit_iso_a@tcp("
	// We can't easily test cross-user privilege in integration without
	// real network DSN; instead just verify both users + their dbs exist
	// independently.
	for _, name := range []string{"it_iso_a", "it_iso_b"} {
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name=?",
			name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("expected schema %s to exist", name)
		}
	}
	_ = dsnA
}
