// Package templates embeds the FreeRADIUS configuration templates that
// radius-manager-api renders when creating an instance.
//
// Templates are placeholders in the v0.1.0 scaffold. Phase 2
// (per SRS §13) will port the full content from radius-manager.sh.
package templates

import (
	"embed"
	"fmt"
	"io"
	"text/template"
)

//go:embed templates/*.tmpl
var fs embed.FS

// Vars carries everything a FreeRADIUS instance template needs to render.
// Field names match what create_sql_module/create_eap_module/etc. in
// radius-manager.sh use, so the bash and Go renderings stay congruent.
type Vars struct {
	InstanceName string
	DBHost       string
	DBPort       int
	DBName       string
	DBUser       string
	DBPass       string
	AuthPort     int
	AcctPort     int
	CoAPort      int
	InnerPort    int
}

// Render writes the named template (e.g., "sql_module") to w with vars.
func Render(w io.Writer, name string, vars Vars) error {
	t, err := template.ParseFS(fs, "templates/"+name+".tmpl")
	if err != nil {
		return fmt.Errorf("parse template %s: %w", name, err)
	}
	return t.Execute(w, vars)
}
