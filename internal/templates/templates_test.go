package templates

import (
	"bytes"
	"strings"
	"testing"
)

func renderTo(t *testing.T, name string, vars Vars) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Render(&buf, name, vars); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q\nFULL OUTPUT:\n%s", w, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(got, b) {
			t.Errorf("output unexpectedly contains %q\nFULL OUTPUT:\n%s", b, got)
		}
	}
}

func standardVars() Vars {
	return Vars{
		InstanceName: "mitra_x",
		DBHost:       "localhost",
		DBPort:       3306,
		DBName:       "mitra_x",
		DBUser:       "mitra_x",
		DBPass:       "secret123",
		AuthPort:     18234,
		AcctPort:     18235,
		CoAPort:      20234,
		InnerPort:    23234,
	}
}

// ============================================
// sql_module
// ============================================

func TestSQLModule_RendersInstanceName(t *testing.T) {
	out := renderTo(t, "sql_module", standardVars())
	mustContain(t, out, "sql sql_mitra_x {")
}

func TestSQLModule_HasDatabaseConnectionFields(t *testing.T) {
	out := renderTo(t, "sql_module", standardVars())
	mustContain(t, out,
		`server    = "localhost"`,
		`port      = 3306`,
		`login     = "mitra_x"`,
		`password  = "secret123"`,
		`radius_db = "mitra_x"`,
	)
}

func TestSQLModule_HasFreeRADIUSDialectVariables(t *testing.T) {
	// FreeRADIUS-side ${dialect} and ${modconfdir} must pass through
	// literally — they are evaluated by FreeRADIUS at runtime, not by
	// our Go template engine.
	out := renderTo(t, "sql_module", standardVars())
	mustContain(t, out,
		`driver  = "rlm_sql_${dialect}"`,
		`$INCLUDE ${modconfdir}/sql/main/${dialect}/queries.conf`,
	)
}

func TestSQLModule_GroupAttributeMatchesInstance(t *testing.T) {
	out := renderTo(t, "sql_module", standardVars())
	mustContain(t, out, `group_attribute = "sql_mitra_x-SQL-Group"`)
}

// ============================================
// eap_module
// ============================================

func TestEAPModule_RendersInstanceName(t *testing.T) {
	out := renderTo(t, "eap_module", standardVars())
	mustContain(t, out,
		"eap eap_mitra_x {",
		"tls-config tls-mitra_x {",
		`virtual_server         = "inner-tunnel-mitra_x"`,
	)
}

func TestEAPModule_HasMschapAndPeap(t *testing.T) {
	out := renderTo(t, "eap_module", standardVars())
	mustContain(t, out,
		"mschapv2 { }",
		"peap {",
		"ttls {",
	)
}

// ============================================
// inner_tunnel
// ============================================

func TestInnerTunnel_BindsToInnerPort(t *testing.T) {
	out := renderTo(t, "inner_tunnel", standardVars())
	mustContain(t, out,
		"server inner-tunnel-mitra_x {",
		"ipaddr = 127.0.0.1",
		"port   = 23234",
		"type   = auth",
	)
}

func TestInnerTunnel_ReferencesInstanceModules(t *testing.T) {
	out := renderTo(t, "inner_tunnel", standardVars())
	mustContain(t, out,
		"eap_mitra_x {",
		"sql_mitra_x",
	)
}

// ============================================
// virtual_server
// ============================================

func TestVirtualServer_HasThreeListeners(t *testing.T) {
	out := renderTo(t, "virtual_server", standardVars())
	mustContain(t, out,
		"server mitra_x {",
		"port   = 18234", // auth
		"port   = 18235", // acct
		"port   = 20234", // coa
		"type   = auth",
		"type   = acct",
		"type   = coa",
	)
}

func TestVirtualServer_ReferencesInstanceModules(t *testing.T) {
	out := renderTo(t, "virtual_server", standardVars())
	mustContain(t, out,
		"eap_mitra_x",
		"sql_mitra_x",
	)
}

func TestVirtualServer_NoServerPrefix(t *testing.T) {
	// Bash comment: "TANPA prefix pppoe_" — the instance name must be
	// used directly as the server name, no prefix.
	out := renderTo(t, "virtual_server", standardVars())
	mustNotContain(t, out, "pppoe_mitra_x")
}

// ============================================
// safety
// ============================================

func TestRender_UnknownTemplateReturnsError(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, "no_such_template", standardVars())
	if err == nil {
		t.Fatal("expected error for unknown template")
	}
}

func TestRender_AllInstanceNameOccurrencesSubstituted(t *testing.T) {
	// Catch double-escaping bugs: no literal "{{ .InstanceName }}"
	// or other Go template noise should leak into output.
	for _, name := range []string{"sql_module", "eap_module", "inner_tunnel", "virtual_server"} {
		out := renderTo(t, name, standardVars())
		mustNotContain(t, out, "{{", "}}", ".InstanceName", ".DBPass")
	}
}
