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
	// BAWAANNYA "0.0.0.0/0" (config.jaringanClientDinamis). Itu keputusan
	// pemilik sistem 29 Sep 2026, diambil setelah imbal-baliknya dijelaskan,
	// dan alasannya struktural: NAS produksi memakai IP PUBLIK sembarang — 24
	// dari 25 radius_servers hidup ber-reach_mode "public", di mana nasname
	// adalah IP publik router mitra atau IP publik concentrator — sehingga
	// TIDAK ADA CIDR yang bisa dideklarasikan di muka.
	//
	// Imbal-baliknya, dan ini nyata: paket dari IP yang TIDAK ada di tabel
	// `nas` memicu pencarian SQL. FreeRADIUS 3 tak punya negative cache (baru
	// ada di v4) dan tak punya deny-list maupun max_clients. Yang menahan
	// biayanya: ia membatasi SATU client baru per detik per blok network, tak
	// pernah menjawab IP tak dikenal (jadi nol amplifikasi refleksi), dan tak
	// pernah mengirim secret ke pengirim.
	//
	// Menyempitkan kalau seluruh NAS sebuah mesin lewat pool VPN: isi CIDR-nya.
	// Mematikan: isi "off". Pengerasan lanjutan kalau port RADIUS sebuah mesin
	// memang terbuka lebar: allow-list kernel (nftables set) yang dibangkitkan
	// dari tabel `nas` dan diperbarui atomik tanpa menyentuh daemon — di luar
	// cakupan berkas ini.
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
