package manager

import (
	"context"
	"testing"
)

// /v1/server/info harus MENGUMUMKAN blok port yang dipakai mesin ini. Tanpa itu
// ERP tidak punya cara memverifikasi VM benar-benar memakai blok yang ia
// perintahkan lewat skrip pemasangan; blok yang keliru baru ketahuan setelah
// ada instance yang tak terjangkau backend (gagal senyap).
func TestServerInfo_MengumumkanBlokPort(t *testing.T) {
	dir := t.TempDir()
	i := &impl{cfg: Config{
		FreeRADIUSDir: dir,
		APIVersion:    "test",
		APIPortStart:  20100,
		Listen:        "0.0.0.0:20000",
	}}

	info, err := i.ServerInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.APIPortStart != 20100 {
		t.Fatalf("api_port_start: got %d want 20100", info.APIPortStart)
	}
	if info.Listen != "0.0.0.0:20000" {
		t.Fatalf("listen: got %q want %q", info.Listen, "0.0.0.0:20000")
	}
}

// Mode read-only / konfigurasi lama tidak mengisi Config.APIPortStart. Angka
// yang diumumkan tetap harus sama dengan yang dipakai saat alokasi, jadi
// registry port jadi sumber cadangan, lalu bawaan.
func TestServerInfo_BlokPortCadanganDariRegistry(t *testing.T) {
	dir := t.TempDir()
	pr := NewPortRegistryWithAPIStart(dir+"/.port_registry", 21100)
	i := &impl{cfg: Config{FreeRADIUSDir: dir, APIVersion: "test", Ports: pr}}

	info, err := i.ServerInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.APIPortStart != 21100 {
		t.Fatalf("api_port_start dari registry: got %d want 21100", info.APIPortStart)
	}

	j := &impl{cfg: Config{FreeRADIUSDir: dir, APIVersion: "test"}}
	info2, err := j.ServerInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info2.APIPortStart != DefaultAPIPortStart {
		t.Fatalf("api_port_start bawaan: got %d want %d", info2.APIPortStart, DefaultAPIPortStart)
	}
}
