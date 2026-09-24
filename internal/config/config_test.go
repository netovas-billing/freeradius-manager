package config

import (
	"path/filepath"
	"testing"

	"github.com/netovas-billing/freeradius-manager/internal/manager"
)

// RM_API_API_PORT_START hanya dibaca di sini (satu tempat baca konfigurasi);
// batas kewajarannya ditegakkan di manager, jadi Load() cukup meneruskan
// angkanya apa adanya dan memberi 0 saat env kosong/bukan angka.
func TestLoad_APIPortStart(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"tidak diisi", "", 0},
		{"blok kustom", "8300", 8300},
		{"bukan angka", "delapanribu", 0},
		{"di luar rentang tetap diteruskan (manager yang menolak)", "70000", 70000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RM_API_TOKEN", "devtoken")
			t.Setenv("RM_API_API_PORT_START", tc.env)

			c, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.APIPortStart != tc.want {
				t.Fatalf("APIPortStart: got %d want %d", c.APIPortStart, tc.want)
			}
		})
	}
}

// "Diisi tapi ditolak" harus bisa dibedakan dari "tidak diisi". Kalau tidak,
// salah ketik (mis. "delapanribu" atau "0") jatuh ke blok port bawaan TANPA
// peringatan apa pun, dan instance lahir di luar rentang port yang di-DSTNAT
// concentrator — provisioning tetap "berhasil", backend tak pernah bisa
// menghubunginya. APIPortStartRaw adalah pembedanya.
func TestLoad_APIPortStartRaw(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    int
		wantRaw string
	}{
		{"tidak diisi -> raw kosong (tidak perlu WARN)", "", 0, ""},
		{"bukan angka -> raw disimpan untuk WARN", "delapanribu", 0, "delapanribu"},
		{"spasi di ujung tetap terbaca", "8100 ", 8100, "8100 "},
		{"nol ditulis eksplisit -> raw disimpan", "0", 0, "0"},
		{"di luar rentang -> raw disimpan (manager yang menolak)", "70000", 70000, "70000"},
		{"batas atas 64000 masih sah", "64000", 64000, "64000"},
		{"hanya spasi dianggap tidak diisi", "   ", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RM_API_TOKEN", "devtoken")
			t.Setenv("RM_API_API_PORT_START", tc.env)

			c, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.APIPortStart != tc.want {
				t.Fatalf("APIPortStart: got %d want %d", c.APIPortStart, tc.want)
			}
			if c.APIPortStartRaw != tc.wantRaw {
				t.Fatalf("APIPortStartRaw: got %q want %q", c.APIPortStartRaw, tc.wantRaw)
			}
		})
	}
}

// Tabel parity: bentuk nilai RM_API_API_PORT_START -> port start EFEKTIF.
//
// Daftar yang sama dijalankan sisi bash di scripts/test-port-block.sh. Wajib
// sama persis: skrip bash dan RM-API Go menulis SATU .port_registry yang sama,
// jadi kalau satu sisi menerima nilai yang ditolak sisi lain, satu mesin
// memakai DUA blok port berbeda. Instance yang lahir di jalur yang jatuh ke
// bawaan 8100 tidak ikut di-DSTNAT concentrator: provisioning tetap
// "berhasil", backend tak pernah bisa menghubunginya, dan dua jalur bisa
// membagikan nomor port yang sama tanpa galat di mana pun.
//
// Aturannya: pangkas HANYA spasi di ujung, lalu terima hanya digit (nol di
// depan boleh, dibaca desimal); rentang wajar 1024-64000 ditegakkan manager.
func TestAPIPortStartParityWithBashTable(t *testing.T) {
	cases := []struct {
		raw  string
		want int // port start efektif setelah klem manager
	}{
		{"", manager.DefaultAPIPortStart},
		{" ", manager.DefaultAPIPortStart},
		{"8100 ", 8100},
		{" 8100", 8100},
		{"0020100", 20100},
		{"20 100", manager.DefaultAPIPortStart}, // spasi di TENGAH ditolak
		{"delapanribu", manager.DefaultAPIPortStart},
		{"0", manager.DefaultAPIPortStart},
		{"1023", manager.DefaultAPIPortStart},
		{"1024", 1024},
		{"64000", 64000},
		{"64100", manager.DefaultAPIPortStart},
		{"70000", manager.DefaultAPIPortStart},
	}
	for _, tc := range cases {
		t.Run("raw="+tc.raw, func(t *testing.T) {
			t.Setenv("RM_API_TOKEN", "devtoken")
			t.Setenv("RM_API_API_PORT_START", tc.raw)

			c, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			r := manager.NewPortRegistryWithAPIStart(
				filepath.Join(t.TempDir(), "ports.txt"), c.APIPortStart)
			if r.APIPortStart != tc.want {
				t.Fatalf("port start efektif untuk %q: got %d want %d", tc.raw, r.APIPortStart, tc.want)
			}
		})
	}
}

// Aturan mentahnya diuji langsung juga, supaya kegagalan menunjuk ke parser
// dan bukan ke klem rentang di manager.
func TestParseAPIPortStart(t *testing.T) {
	cases := []struct {
		raw    string
		want   int
		wantOK bool
	}{
		{"", 0, false},
		{" ", 0, false},
		{"8100 ", 8100, true},
		{" 8100", 8100, true},
		{"0020100", 20100, true},
		{"20 100", 0, false},
		{"delapanribu", 0, false},
		{"0", 0, true},
		{"1023", 1023, true},
		{"1024", 1024, true},
		{"64000", 64000, true},
		{"64100", 64100, true},
		{"70000", 70000, true},
		{"+8100", 0, false},
		{"-8100", 0, false},
		{"999999", 0, false}, // lebih dari 5 digit: mustahil jadi nomor port
		{"00000", 0, true},
	}
	for _, tc := range cases {
		t.Run("raw="+tc.raw, func(t *testing.T) {
			got, ok := ParseAPIPortStart(tc.raw)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("ParseAPIPortStart(%q): got (%d,%v) want (%d,%v)",
					tc.raw, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
