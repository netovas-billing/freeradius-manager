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
	for _, terlarang := range []string{"dynamic_clients", "dynclients_", "client dynamic_"} {
		if strings.Contains(s, terlarang) {
			t.Errorf("DynamicClientNet kosong tapi %q ikut dipancarkan:\n%s", terlarang, s)
		}
	}
}

// Diisi = blok client ada DI DALAM listen, bukan global.
//
// Satu FreeRADIUS melayani BANYAK instance di mesin ini, dan yang membedakannya
// adalah PORT — bukan alamat NAS. Blok client global membuat paket dari satu NAS
// bisa dicocokkan instance mana pun yang jaringannya bertumpang tindih, dan yang
// menang adalah yang kebetulan lebih dulu: pelanggan mitra A diautentikasi
// terhadap data mitra B.
func TestDynamicClients_ClientBeradaDiDalamListen(t *testing.T) {
	s := render(t, "172.31.199.0/24")
	iAuth := strings.Index(s, "port   = 14368")
	iClient := strings.Index(s, "client dynamic_radius_test1 {")
	iAuthorize := strings.Index(s, "    authorize {")
	if iAuth < 0 || iClient < 0 || iAuthorize < 0 {
		t.Fatalf("potongan yang dicari tidak ada:\n%s", s)
	}
	if !(iAuth < iClient && iClient < iAuthorize) {
		t.Fatalf("client dinamis tidak berada di dalam blok listen auth "+
			"(auth=%d client=%d authorize=%d)", iAuth, iClient, iAuthorize)
	}
	if !strings.Contains(s, "ipaddr          = 172.31.199.0/24") {
		t.Errorf("jaringan tidak dipancarkan apa adanya:\n%s", s)
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
