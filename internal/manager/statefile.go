package manager

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/netovas-billing/freeradius-manager/pkg/types"
)

// readWholeFile reads a file's contents in one shot. Used by the
// statefile tests; kept here so production callers can also use it.
func readWholeFile(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// instanceFile represents the .instance_<nama> file produced by
// radius-manager.sh. Format documented in PRD.md Lampiran A.
//
// Both radius-manager.sh (bash) and radius-manager-api (Go) read/write
// this file. Format is strict KEY=VALUE, no shell expansion.
type instanceFile struct {
	AdminUsername   string
	DBHost          string
	DBPort          int
	DBName          string
	DBUser          string
	DBPass          string
	AuthPort        int
	AcctPort        int
	CoAPort         int
	InnerPort       int
	APIPort         int
	SwaggerUsername string
	SwaggerPassword string
	WebAPIURL       string
	Created         time.Time
}

func (i *impl) instanceFilePath(name string) string {
	return filepath.Join(i.cfg.FreeRADIUSDir, ".instance_"+name)
}

func (i *impl) readInstanceFile(name string) (*instanceFile, error) {
	path := i.instanceFilePath(name)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrInstanceNotFound
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	out := &instanceFile{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// strip surrounding quotes if any
		val = strings.Trim(val, `"'`)

		switch key {
		case "ADMIN_USERNAME":
			out.AdminUsername = val
		case "DB_HOST":
			out.DBHost = val
		case "DB_PORT":
			out.DBPort, _ = strconv.Atoi(val)
		case "DB_NAME":
			out.DBName = val
		case "DB_USER":
			out.DBUser = val
		case "DB_PASS":
			out.DBPass = val
		case "AUTH_PORT":
			out.AuthPort, _ = strconv.Atoi(val)
		case "ACCT_PORT":
			out.AcctPort, _ = strconv.Atoi(val)
		case "COA_PORT":
			out.CoAPort, _ = strconv.Atoi(val)
		case "INNER_PORT":
			out.InnerPort, _ = strconv.Atoi(val)
		case "API_PORT":
			out.APIPort, _ = strconv.Atoi(val)
		case "SWAGGER_USERNAME":
			out.SwaggerUsername = val
		case "SWAGGER_PASSWORD":
			out.SwaggerPassword = val
		case "WEB_API_URL":
			out.WebAPIURL = val
		case "CREATED":
			if t, err := time.Parse(time.RFC3339, val); err == nil {
				out.Created = t
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if out.DBName == "" {
		return nil, fmt.Errorf("instance file %s missing DB_NAME", path)
	}
	return out, nil
}

// writeInstanceFile writes the .instance_<name> file in the format that
// load_instance_info() in radius-manager.sh can parse safely.
//
// Format rules (must NOT change without coordinating bash side):
//   - KEY=VALUE, no whitespace around =
//   - VALUE for fields containing colons or shell metacharacters MUST be
//     quoted to prevent shell evaluation when bash uses `source`.
//   - One key per line, trailing newline.
func (i *impl) writeInstanceFile(name string, in *instanceFile) error {
	path := i.instanceFilePath(name)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", path, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	w := bufio.NewWriter(f)
	write := func(k, v string) {
		fmt.Fprintf(w, "%s=%s\n", k, v)
	}
	writeQ := func(k, v string) {
		fmt.Fprintf(w, "%s=%q\n", k, v)
	}
	writeInt := func(k string, v int) {
		fmt.Fprintf(w, "%s=%d\n", k, v)
	}

	write("ADMIN_USERNAME", in.AdminUsername)
	write("DB_HOST", defaultStr(in.DBHost, "localhost"))
	writeInt("DB_PORT", defaultInt(in.DBPort, 3306))
	write("DB_NAME", in.DBName)
	write("DB_USER", in.DBUser)
	write("DB_PASS", in.DBPass)
	writeInt("AUTH_PORT", in.AuthPort)
	writeInt("ACCT_PORT", in.AcctPort)
	writeInt("COA_PORT", in.CoAPort)
	writeInt("INNER_PORT", in.InnerPort)
	writeInt("API_PORT", in.APIPort)
	write("SWAGGER_USERNAME", defaultStr(in.SwaggerUsername, "admin"))
	write("SWAGGER_PASSWORD", in.SwaggerPassword)
	write("WEB_API_URL", in.WebAPIURL)
	// CREATED contains colons; always quote.
	created := in.Created
	if created.IsZero() {
		created = time.Now().UTC()
	}
	writeQ("CREATED", created.UTC().Format(time.RFC3339))

	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush %s: %w", path, err)
	}
	return f.Sync()
}

func defaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func defaultInt(n, d int) int {
	if n == 0 {
		return d
	}
	return n
}

func (f *instanceFile) toInstance(includeSecrets bool) *types.Instance {
	inst := &types.Instance{
		Name:    f.AdminUsername,
		Status:  types.StatusUnknown,
		Enabled: false,
		Ports: types.Ports{
			Auth:  f.AuthPort,
			Acct:  f.AcctPort,
			CoA:   f.CoAPort,
			Inner: f.InnerPort,
			API:   f.APIPort,
		},
		Database: types.Database{
			Host:          f.DBHost,
			Port:          f.DBPort,
			Name:          f.DBName,
			User:          f.DBUser,
			PasswordKnown: f.DBPass != "",
		},
		Swagger: types.Credentials{
			Username:      f.SwaggerUsername,
			PasswordKnown: f.SwaggerPassword != "",
		},
		APIURL:    strings.TrimSuffix(f.WebAPIURL, "/docs"),
		CreatedAt: f.Created,
	}
	if includeSecrets {
		inst.Database.Password = f.DBPass
		inst.Swagger.Password = f.SwaggerPassword
	}
	return inst
}
