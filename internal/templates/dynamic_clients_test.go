package templates

import (
	"bytes"
	"strings"
	"testing"
)

func render(t *testing.T, net string) string {
	t.Helper()
	var b bytes.Buffer
	v := Vars{
		InstanceName: "radius_test1", DBHost: "127.0.0.1", DBPort: 3306,
		DBName: "radius_test1", DBUser: "radius_test1", DBPass: "x",
		AuthPort: 14368, AcctPort: 14369, CoAPort: 16368, InnerPort: 19368,
		DynamicClientNet: net,
	}
	if err := Render(&b, "virtual_server", v); err != nil {
		t.Fatalf("render: %v", err)
	}
	return b.String()
}

// KOSONG = tidak ada yang berubah.
//
// Ini menyentuh konfigurasi FreeRADIUS SETIAP instance, dan template yang salah
// berarti tak ada instance yang jalan. Karena itu fiturnya opt-in: selama
// operator tidak menyebut jaringannya, konfigurasinya persis seperti sebelumnya.
func TestDynamicClients_KosongTakMengubahApaPun(t *testing.T) {
	s := render(t, "")
	for _, terlarang := range []string{"dynamic_clients", "dynclients_", "clients dynclist_", "clients = dynclist_"} {
		if strings.Contains(s, terlarang) {
			t.Errorf("DynamicClientNet kosong tapi %q ikut dipancarkan:\n%s", terlarang, s)
		}
	}
}

// Diisi = blok client ada DI DALAM listen, bukan global.
//
// Bentuk yang dipakai HARUS `clients = <nama>` pada listener + blok
// `clients <nama> { client ... }` di lingkup global — bukan `client { }` yang
// disarangkan di dalam `listen { }`.
//
// Alasannya ada di source FreeRADIUS, bukan selera:
//
//   - client.c:248-251 — listener TANPA item `clients` membuat client dari SQL
//     masuk ke daftar GLOBAL, sementara `client { }` di dalam `server { }`
//     memberi listener itu daftar LAIN. Dua daftar berbeda, dan paketnya tetap
//     dibuang. Itu cacat bentuk lama.
//
//   - client.c:278 — routing hanya membaca seksi `listen` PERTAMA (komentarnya
//     sendiri: "@todo - add the client to _all_ listeners?"). Jadi auth, acct,
//     dan coa WAJIB menunjuk nama daftar yang SAMA; kalau berbeda, auth hidup
//     tapi accounting dibuang senyap — jalur uang mati tanpa pesan.
//
//   - Daftar per-socket juga memisahkan client SQL antar instance. Tanpa itu
//     semua instance menumpuk di satu daftar global, dan karena concentrator
//     adalah sumber daya bersama dua mitra bisa punya nasname IDENTIK: hanya
//     satu secret yang bertahan, mitra yang kalah mati tanpa pesan.
func TestDynamicClients_DaftarPerSocketBukanNestedDiListen(t *testing.T) {
	s := render(t, "172.31.199.0/24")

	// Bentuk lama yang tak berdasar tidak boleh kembali.
	if strings.Contains(s, "client dynamic_radius_test1 {") {
		t.Error("kembali ke `client { }` yang disarangkan di dalam `listen { }` — bentuk itu tak didukung")
	}

	iClients := strings.Index(s, "clients dynclist_radius_test1 {")
	iServer := strings.Index(s, "server radius_test1 {")
	if iClients < 0 || iServer < 0 {
		t.Fatalf("blok clients atau server tidak ada:\n%s", s)
	}
	// Lingkup GLOBAL: blok clients harus di luar `server { }`.
	if iClients > iServer {
		t.Errorf("blok clients ada di dalam server (clients=%d server=%d) — harus lingkup global", iClients, iServer)
	}

	// KETIGA listener menunjuk daftar yang sama. Ini penjaga client.c:278.
	if n := strings.Count(s, "clients = dynclist_radius_test1"); n != 3 {
		t.Errorf("listener yang menunjuk daftar per-socket = %d, mau 3 (auth, acct, coa):\n%s", n, s)
	}
	for _, tipe := range []string{"auth", "acct", "coa"} {
		i := strings.Index(s, "type   = "+tipe)
		if i < 0 {
			t.Fatalf("listener %s tidak ada", tipe)
		}
		// Cari `clients =` berikutnya SEBELUM listener/blok berikutnya dibuka.
		sisa := s[i:]
		batas := strings.Index(sisa[1:], "    listen {")
		if batas < 0 {
			batas = strings.Index(sisa, "    authorize {")
		}
		if batas > 0 {
			sisa = sisa[:batas]
		}
		if !strings.Contains(sisa, "clients = dynclist_radius_test1") {
			t.Errorf("listener %s tidak menunjuk dynclist — auth/acct/coa harus daftar yang SAMA", tipe)
		}
	}

	if !strings.Contains(s, "ipaddr          = 172.31.199.0/24") {
		t.Errorf("jaringan tidak dipancarkan apa adanya:\n%s", s)
	}
}

// `lifetime = 0` berarti client dinamis di-cache sampai restart, sehingga GANTI
// SECRET dan HAPUS NAS tetap butuh restart — dua kasus yang justru ingin kita
// hilangkan. Config lama (freeradius-api 340102c) memakai 0; di sini tidak.
func TestDynamicClients_LifetimeTidakAbadi(t *testing.T) {
	s := render(t, "0.0.0.0/0")
	i := strings.Index(s, "clients dynclist_radius_test1 {")
	if i < 0 {
		t.Fatalf("blok clients tidak ada:\n%s", s)
	}
	blok := s[i:]
	if j := strings.Index(blok, "\n}"); j > 0 {
		blok = blok[:j]
	}
	if strings.Contains(blok, "lifetime        = 0") {
		t.Error("lifetime = 0 — ganti secret dan hapus NAS akan tetap butuh restart")
	}
	if !strings.Contains(blok, "lifetime        = 600") {
		t.Errorf("lifetime bukan 600:\n%s", blok)
	}
}

// DUA hal yang paling mudah salah saat menyalin contoh FreeRADIUS.
func TestDynamicClients_ModulDanVirtualServerPerInstance(t *testing.T) {
	s := render(t, "172.31.199.0/24")

	// 1. Modul SQL harus milik instance ini — `sql` polos akan mencari NAS di
	//    database instance yang SALAH.
	if strings.Contains(s, "%{sql:") {
		t.Error("memakai modul `sql` polos — NAS akan dicari di database instance lain")
	}
	if !strings.Contains(s, "%{sql_radius_test1: SELECT secret FROM nas") {
		t.Errorf("lookup secret tidak memakai modul per-instance:\n%s", s)
	}

	// 2. Virtual server tujuan harus instance ini — "default" akan memproses
	//    permintaan mitra A dengan data mitra B.
	if strings.Contains(s, `FreeRADIUS-Client-Virtual-Server = "default"`) {
		t.Error(`Virtual-Server = "default" — permintaan diproses virtual server yang salah`)
	}
	if !strings.Contains(s, `FreeRADIUS-Client-Virtual-Server = "radius_test1"`) {
		t.Errorf("Virtual-Server bukan instance ini:\n%s", s)
	}
}

// Jaringan seluas 0.0.0.0/0 diteruskan apa adanya kalau operator memaksa, tapi
// bawaannya tak pernah begitu — nilai itu HARUS datang dari env, bukan dari sini.
func TestDynamicClients_TakAdaBawaanSeluasApaPun(t *testing.T) {
	s := render(t, "")
	if strings.Contains(s, "0.0.0.0/0") {
		t.Errorf("template memancarkan 0.0.0.0/0 sebagai bawaan:\n%s", s)
	}
}
