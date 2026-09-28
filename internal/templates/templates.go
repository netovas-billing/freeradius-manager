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

	// DynamicClientNet — jaringan tempat NAS boleh didefinisikan DINAMIS,
	// mis. "172.31.199.0/24" (pool VPN concentrator).
	//
	// KOSONG = fitur MATI dan konfigurasinya persis seperti sebelumnya. Itu
	// bawaan yang disengaja: ini menyentuh konfigurasi FreeRADIUS setiap
	// instance, jadi ia hanya berlaku kalau operator menyebut jaringannya.
	//
	// Kenapa ada: `read_clients = yes` memuat daftar NAS dari SQL HANYA saat
	// FreeRADIUS start. NAS yang ditambahkan sesudah itu adalah client tak
	// dikenal, dan FreeRADIUS MEMBUANG paketnya tanpa balasan dan tanpa log —
	// gejalanya "radius timeout" di router, yang juga gejala belasan hal lain.
	// Tak ada satu pun jalur kode yang me-restart FreeRADIUS saat NAS ditambah
	// (RM-API RestartInstance hanya menyentuh <nama>-api.service), jadi selama
	// ini itu langkah MANUAL yang harus diketahui orangnya.
	//
	// JANGAN diisi 0.0.0.0/0. Cakupan seluas itu membuat siapa pun yang
	// menjangkau port RADIUS bisa menyuntik accounting (jalur uang) dan menguji
	// kredensial pelanggan, dan satu secret bocor membuka semua NAS sekaligus.
	DynamicClientNet string
}

// Render writes the named template (e.g., "sql_module") to w with vars.
func Render(w io.Writer, name string, vars Vars) error {
	t, err := template.ParseFS(fs, "templates/"+name+".tmpl")
	if err != nil {
		return fmt.Errorf("parse template %s: %w", name, err)
	}
	return t.Execute(w, vars)
}
