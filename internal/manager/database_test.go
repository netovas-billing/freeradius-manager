package manager

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func newMockDB(t *testing.T) (*DBManager, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	dm := &DBManager{
		DB:           db,
		AllowRemote:  true,
		RemoteHost:   "%",
	}
	return dm, mock, func() { db.Close() }
}

func TestDBManager_CreateDatabase_NewDatabase(t *testing.T) {
	dm, mock, cleanup := newMockDB(t)
	defer cleanup()

	// Idempotency check first: USE returns error → not exists.
	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := dm.CreateDatabase(context.Background(), "mitra_x"); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestDBManager_CreateDatabase_AlreadyExists_Skips(t *testing.T) {
	dm, mock, cleanup := newMockDB(t)
	defer cleanup()

	// USE succeeds → already exists; CreateDatabase must not re-issue CREATE.
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))

	if err := dm.CreateDatabase(context.Background(), "mitra_x"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestDBManager_CreateUserAndGrant_LocalhostAndRemote(t *testing.T) {
	dm, mock, cleanup := newMockDB(t)
	defer cleanup()
	dm.AllowRemote = true

	// localhost user count → 0 → CREATE
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost' IDENTIFIED BY 'pw'").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE ON `mitra_x`\\.\\* TO 'mitra_x'@'localhost'").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// remote user count → 0 → CREATE
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='%'`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'%' IDENTIFIED BY 'pw'").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE ON `mitra_x`\\.\\* TO 'mitra_x'@'%'").
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))

	if err := dm.CreateUserAndGrant(context.Background(), "mitra_x", "mitra_x", "pw"); err != nil {
		t.Fatalf("CreateUserAndGrant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestDBManager_CreateUserAndGrant_AlreadyExists_AltersPassword(t *testing.T) {
	dm, mock, cleanup := newMockDB(t)
	defer cleanup()
	dm.AllowRemote = false

	// localhost user count → 1 → ALTER not CREATE
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec("ALTER USER 'mitra_x'@'localhost' IDENTIFIED BY 'newpw'").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE ON `mitra_x`\\.\\* TO 'mitra_x'@'localhost'").
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))

	if err := dm.CreateUserAndGrant(context.Background(), "mitra_x", "mitra_x", "newpw"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestDBManager_DropDatabase_RemovesAll(t *testing.T) {
	dm, mock, cleanup := newMockDB(t)
	defer cleanup()
	dm.AllowRemote = true

	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DROP DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec("DROP USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='%'`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec("DROP USER 'mitra_x'@'%'").WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))

	if err := dm.DropDatabase(context.Background(), "mitra_x", "mitra_x"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestDBManager_RejectsInvalidNameForBacktickInjection(t *testing.T) {
	dm, _, cleanup := newMockDB(t)
	defer cleanup()

	bad := []string{
		"foo`bar",       // backtick injection
		"foo' OR '1",    // quote injection
		"foo;DROP",      // statement separator
		"foo bar",       // whitespace
		"",              // empty
	}
	for _, name := range bad {
		if err := dm.CreateDatabase(context.Background(), name); err == nil {
			t.Errorf("expected rejection for %q", name)
		}
	}
}

func TestDBManager_ImportSchema_AppliesAllMigrations(t *testing.T) {
	dm, mock, cleanup := newMockDB(t)
	defer cleanup()

	migrations := []Migration{
		{Name: "001_init.sql", SQL: "CREATE TABLE radcheck (id INT)"},
		{Name: "002_extra.sql", SQL: "CREATE TABLE radacct (id INT)"},
	}

	// Probe whether radcheck exists first → empty rows = no schema yet.
	mock.ExpectQuery("SHOW TABLES FROM `mitra_x` LIKE 'radcheck'").
		WillReturnRows(sqlmock.NewRows([]string{"tbl"}))

	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE radcheck (id INT)")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE radacct (id INT)")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := dm.ImportSchema(context.Background(), "mitra_x", migrations); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestDBManager_ImportSchema_SkipsIfRadcheckExists(t *testing.T) {
	dm, mock, cleanup := newMockDB(t)
	defer cleanup()

	mock.ExpectQuery("SHOW TABLES FROM `mitra_x` LIKE 'radcheck'").
		WillReturnRows(sqlmock.NewRows([]string{"tbl"}).AddRow("radcheck"))

	if err := dm.ImportSchema(context.Background(), "mitra_x",
		[]Migration{{Name: "x", SQL: "SELECT 1"}}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// Migration mirrors internal/schema.Migration so the manager package
// can accept migrations from any source without an import cycle.
// Defined in database.go.
