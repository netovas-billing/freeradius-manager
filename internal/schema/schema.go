// Package schema embeds the FreeRADIUS MariaDB schema files. Migration
// files are applied in lexical order (001_, 002_, ...) on instance create.
//
// See SRS §15.1 (RM-Q06 decision).
package schema

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migration is one ordered SQL file with its content already loaded.
type Migration struct {
	Name string
	SQL  string
}

// All returns every migration in lexical order so callers can iterate
// and apply them with database/sql Exec.
func All() ([]Migration, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := make([]Migration, 0, len(names))
	for _, n := range names {
		b, err := migrations.ReadFile("migrations/" + n)
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Name: n, SQL: string(b)})
	}
	return out, nil
}
