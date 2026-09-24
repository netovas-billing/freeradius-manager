package config

import (
	"fmt"

	"github.com/go-sql-driver/mysql"
)

// NormalisasiDSN — pastikan DSN MariaDB menyalakan hal-hal yang dibutuhkan
// RM-API, apa pun yang ditulis operator di RM_API_DB_DSN.
//
// multiStatements WAJIB. Skema FreeRADIUS diterapkan sebagai SATU berkas
// (internal/schema/migrations/001_init.sql) lewat satu ExecContext, dan berkas
// itu berisi belasan CREATE TABLE. Tanpa multiStatements, driver mengirimkan
// seluruh berkas sebagai satu statement; server mengurai CREATE TABLE pertama
// lalu tersandung tepat di awal yang kedua:
//
//	Error 1064 (42000): You have an error in your SQL syntax ... near
//	'CREATE TABLE IF NOT EXISTS radcheck (' at line 68
//
// Pesan itu menunjuk baris 68 seolah berkasnya yang salah, padahal berkasnya
// benar — yang kurang satu parameter koneksi. Terjadi nyata 24 Sep 2026 saat
// instance pertama dibuat; jalur bash tidak pernah kena karena `mysql < file`
// memang memecah statement sendiri.
//
// Ditegakkan di sini, bukan di berkas env, supaya DSN yang disunting operator
// tidak bisa mematikannya tanpa sengaja.
//
// DSN yang tak bisa diurai dikembalikan APA ADANYA beserta galatnya: menebak
// bentuk yang benar lebih berbahaya daripada membiarkan koneksi gagal dengan
// pesan driver yang asli.
func NormalisasiDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", nil
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return dsn, fmt.Errorf("RM_API_DB_DSN tidak bisa diurai: %w", err)
	}
	cfg.MultiStatements = true
	return cfg.FormatDSN(), nil
}
