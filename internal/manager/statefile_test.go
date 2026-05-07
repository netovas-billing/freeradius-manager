package manager

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestImpl(t *testing.T) (*impl, string) {
	t.Helper()
	dir := t.TempDir()
	i := &impl{cfg: Config{FreeRADIUSDir: dir, APIVersion: "test"}}
	return i, dir
}

func TestStateFile_RoundTrip(t *testing.T) {
	i, _ := newTestImpl(t)
	in := &instanceFile{
		AdminUsername:   "mitra_x",
		DBHost:          "localhost",
		DBPort:          3306,
		DBName:          "mitra_x",
		DBUser:          "mitra_x",
		DBPass:          "Sup3rSecret!@#$%",
		AuthPort:        18234,
		AcctPort:        18235,
		CoAPort:         20234,
		InnerPort:       23234,
		APIPort:         8112,
		SwaggerUsername: "admin",
		SwaggerPassword: "Sw4ggerP4ss",
		WebAPIURL:       "http://10.254.252.2:8112/docs",
		Created:         time.Date(2026, 5, 7, 10, 23, 45, 0, time.UTC),
	}

	if err := i.writeInstanceFile("mitra_x", in); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := i.readInstanceFile("mitra_x")
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if *out != *in {
		t.Fatalf("round-trip mismatch:\n  got:  %+v\n  want: %+v", out, in)
	}
}

func TestStateFile_BashCompatibleFormat(t *testing.T) {
	i, dir := newTestImpl(t)
	in := &instanceFile{
		AdminUsername: "abc",
		DBName:        "abc",
		DBUser:        "abc",
		Created:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := i.writeInstanceFile("abc", in); err != nil {
		t.Fatal(err)
	}

	// File must use KEY=VALUE without spaces around = so bash IFS='=' read works.
	data, err := readFile(filepath.Join(dir, ".instance_abc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			t.Fatalf("malformed line %q", line)
		}
		k, _, _ := strings.Cut(line, "=")
		if strings.ContainsAny(k, " \t") {
			t.Fatalf("key contains whitespace, breaks bash IFS='=': %q", line)
		}
	}
}

func TestStateFile_CreatedQuoted(t *testing.T) {
	// CREATED contains colons (ISO time). Without quoting, bash that
	// `source`s the file would try to execute "05:57:50" as a command.
	// Our format must always quote the value to prevent that.
	i, dir := newTestImpl(t)
	in := &instanceFile{
		AdminUsername: "x",
		DBName:        "x",
		DBUser:        "x",
		Created:       time.Date(2026, 5, 7, 5, 57, 50, 0, time.UTC),
	}
	if err := i.writeInstanceFile("x", in); err != nil {
		t.Fatal(err)
	}
	data, _ := readFile(filepath.Join(dir, ".instance_x"))
	if !strings.Contains(data, `CREATED="2026-05-07T05:57:50Z"`) {
		t.Fatalf("expected CREATED to be quoted, got:\n%s", data)
	}
}

// readFile is a small helper — keeps tests free of os/io noise.
func readFile(p string) (string, error) {
	b, err := readWholeFile(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
