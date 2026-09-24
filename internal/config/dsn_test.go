package config

import (
	"strings"
	"testing"
)

// multiStatements WAJIB menyala, apa pun yang ditulis operator.
//
// Tanpa itu, penerapan skema FreeRADIUS gagal dengan pesan yang menuduh
// berkas SQL-nya:
//
//	Error 1064 ... near 'CREATE TABLE IF NOT EXISTS radcheck (' at line 68
//
// Berkasnya benar; yang kurang satu parameter koneksi. Terjadi nyata
// 24 Sep 2026 saat instance pertama dibuat.
func TestNormalisasiDSN_MenyalakanMultiStatements(t *testing.T) {
	for _, dsn := range []string{
		"root@unix(/var/run/mysqld/mysqld.sock)/", // bawaan install.sh
		"root:sandi@tcp(127.0.0.1:3306)/radius",   // bentuk TCP
		"root@unix(/var/run/mysqld/mysqld.sock)/?charset=utf8mb4",
		"root@unix(/var/run/mysqld/mysqld.sock)/?multiStatements=false", // dimatikan operator
	} {
		got, err := NormalisasiDSN(dsn)
		if err != nil {
			t.Fatalf("%s: %v", dsn, err)
		}
		if !strings.Contains(got, "multiStatements=true") {
			t.Fatalf("%s → %s: multiStatements tidak menyala", dsn, got)
		}
	}
}

// Yang lain tidak boleh ikut berubah — DSN operator dipakai apa adanya
// selain satu parameter itu.
func TestNormalisasiDSN_TidakMengubahSisanya(t *testing.T) {
	got, err := NormalisasiDSN("radius:rahasia@tcp(10.0.0.5:3307)/radiusdb?charset=utf8mb4&parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	for _, mau := range []string{"radius:rahasia@", "tcp(10.0.0.5:3307)", "/radiusdb", "charset=utf8mb4", "parseTime=true"} {
		if !strings.Contains(got, mau) {
			t.Fatalf("%q hilang dari hasil: %s", mau, got)
		}
	}
}

// DSN kosong = mode read-only; jangan dikarang isinya.
func TestNormalisasiDSN_KosongTetapKosong(t *testing.T) {
	got, err := NormalisasiDSN("")
	if err != nil || got != "" {
		t.Fatalf("got=%q err=%v — DSN kosong harus tetap kosong", got, err)
	}
}

// DSN tak terurai dikembalikan apa adanya BESERTA galatnya: menebak bentuk
// yang benar lebih berbahaya daripada gagal dengan pesan driver yang asli.
func TestNormalisasiDSN_TakTeruraiMelaporkanGalat(t *testing.T) {
	if _, err := NormalisasiDSN("ini-bukan-dsn"); err == nil {
		t.Fatal("DSN ngawur diterima diam-diam")
	}
}
