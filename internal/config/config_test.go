package config

import (
	"path/filepath"
	"strings"
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

// Bawaan runtime freeradius-api = "go".
//
// Ini bukan preferensi gaya: permintaannya adalah instance BARU memakai Go.
// Sebelumnya bawaannya "python" sehingga `create` tetap memasang aplikasi Python
// dan sakelar Go tak pernah terpakai oleh siapa pun yang tidak menyetel env —
// yaitu semua orang. Kalau baris ini berbalik, gejalanya persis itu lagi:
// tak ada galat, semuanya "berhasil", tapi yang terpasang Python.
func TestAPIRuntime_BawaanGo(t *testing.T) {
	t.Setenv("RM_API_TOKEN", "devtoken")
	t.Setenv("RM_API_RUNTIME", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.APIRuntime != "go" {
		t.Fatalf("bawaan RM_API_RUNTIME harus \"go\", dapat %q", c.APIRuntime)
	}
}

// Dan jalan keluarnya harus tetap ada: satu mesin boleh dipaksa kembali ke
// Python tanpa menyunting kode.
func TestAPIRuntime_BisaDipaksaPython(t *testing.T) {
	t.Setenv("RM_API_TOKEN", "devtoken")
	t.Setenv("RM_API_RUNTIME", "python")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.APIRuntime != "python" {
		t.Fatalf("RM_API_RUNTIME=python tidak dihormati: %q", c.APIRuntime)
	}
}

// Repo Go WAJIB punya bawaan.
//
// Bawaan runtime kini "go", dan EnsureTemplate gagal keras kalau repo-nya kosong
// — jadi tanpa bawaan di sini, setiap `create` di mesin yang tidak menyetel
// RM_API_GO_REPO akan gagal total.
func TestBootstrapGoRepo_AdaBawaannya(t *testing.T) {
	t.Setenv("RM_API_TOKEN", "devtoken")
	t.Setenv("RM_API_GO_REPO", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BootstrapGoRepo == "" {
		t.Fatal("RM_API_GO_REPO tanpa bawaan — setiap create akan gagal keras")
	}
	if !strings.Contains(c.BootstrapGoRepo, "netovas-billing/freeradius-api") {
		t.Fatalf("bawaan repo Go bukan repo org: %q", c.BootstrapGoRepo)
	}
}

// Bind BAWAAN harus 0.0.0.0, bukan loopback.
//
// Model pemakaiannya: backend menjangkau VM lewat DSTNAT di IP publik
// concentrator, masuk melalui IP TUNNEL. Bind loopback menolak trafik itu, dan
// kegagalannya TAK TERLIHAT dari dalam VM — service hidup, /health lokal 200,
// self-test installer lulus karena ia menguji 127.0.0.1 dari dalam VM sendiri.
// Yang terlihat operator cuma "backend tak bisa menghubungi RADIUS".
// Terjadi 28 Sep 2026 pada VM radius test1.
func TestListen_BawaanBukanLoopback(t *testing.T) {
	t.Setenv("RM_API_TOKEN", "devtoken")
	t.Setenv("RM_API_LISTEN", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(c.Listen, "127.") || strings.HasPrefix(c.Listen, "localhost") {
		t.Fatalf("bind bawaan loopback (%q) — trafik dari IP tunnel akan ditolak tanpa jejak", c.Listen)
	}
	if !strings.HasPrefix(c.Listen, "0.0.0.0:") {
		t.Fatalf("bind bawaan seharusnya 0.0.0.0:<port>, dapat %q", c.Listen)
	}
}

// Dan nilai yang diisi operator tetap dihormati — termasuk loopback, kalau itu
// memang yang dia mau untuk mesin yang backend-nya satu host.
func TestListen_BisaDitimpa(t *testing.T) {
	t.Setenv("RM_API_TOKEN", "devtoken")
	t.Setenv("RM_API_LISTEN", "0.0.0.0:20000")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:20000" {
		t.Fatalf("RM_API_LISTEN tidak dihormati: %q", c.Listen)
	}
}

// Bawaan DynamicClientNet adalah "0.0.0.0/0" — KEPUTUSAN PEMILIK SISTEM
// 29 Sep 2026, diambil setelah imbal-balik keamanannya dijelaskan. Tes ini ada
// supaya tak ada yang mengembalikannya diam-diam sebagai "pengerasan", dan
// supaya alasannya ikut terbaca:
//
// NAS produksi memakai IP PUBLIK sembarang — 24 dari 25 radius_servers hidup
// ber-reach_mode "public", di mana nasname adalah IP publik router mitra atau
// IP publik concentrator. TIDAK ADA CIDR yang bisa dideklarasikan di muka.
// Dengan CIDR sempit, NAS di luarnya jadi client tak dikenal dan paketnya
// DIBUANG tanpa balasan dan tanpa log sampai FreeRADIUS di-restart — dan satu
// restart di host produksi terpadat menurunkan 9 instance milik 10 tenant.
//
// Yang menahan biayanya: FreeRADIUS 3 membatasi satu client baru per detik per
// blok network, tak pernah menjawab IP tak dikenal (jadi nol amplifikasi
// refleksi), dan tak pernah mengirim secret ke pengirim. Pengerasan lanjutan
// kalau diminta: allow-list nftables yang dibangkitkan dari tabel `nas`.
//
// Kalau seluruh NAS sebuah mesin memang lewat pool VPN, sempitkan lewat env —
// bukan dengan mengubah bawaan ini.
func TestDynamicClientNet_BawaanLuasDisengaja(t *testing.T) {
	t.Setenv("RM_API_TOKEN", "devtoken")
	t.Setenv("RM_API_DYNAMIC_CLIENT_NET", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.DynamicClientNet != "0.0.0.0/0" {
		t.Errorf("bawaan DynamicClientNet = %q, mau %q — lihat komentar di atas "+
			"sebelum mengubahnya", c.DynamicClientNet, "0.0.0.0/0")
	}
}

// Env tetap menang, supaya operator bisa menyempitkan per mesin.
func TestDynamicClientNet_EnvMenimpaBawaan(t *testing.T) {
	t.Setenv("RM_API_TOKEN", "devtoken")
	t.Setenv("RM_API_DYNAMIC_CLIENT_NET", "172.31.199.0/24")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.DynamicClientNet != "172.31.199.0/24" {
		t.Errorf("DynamicClientNet = %q, env diabaikan", c.DynamicClientNet)
	}
}

// Sakelar MATI harus ada. Tanpa penanda khusus, fitur ini tak bisa dimatikan
// lewat env sama sekali (getenv memperlakukan kosong sebagai "tak diisi"), dan
// satu-satunya jalan keluar bagi operator adalah mengubah kode lalu deploy
// ulang — terlalu mahal untuk sesuatu yang menyentuh config FreeRADIUS setiap
// instance di mesin.
func TestDynamicClientNet_SakelarMati(t *testing.T) {
	for _, nilai := range []string{"off", "OFF", "none", "-", "mati", " off "} {
		t.Run(nilai, func(t *testing.T) {
			t.Setenv("RM_API_TOKEN", "devtoken")
			t.Setenv("RM_API_DYNAMIC_CLIENT_NET", nilai)

			c, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if c.DynamicClientNet != "" {
				t.Errorf("%q tidak mematikan fitur (dapat %q)", nilai, c.DynamicClientNet)
			}
		})
	}
}

// Spasi harus dibuang. Nilai " " yang lolos ke template menghasilkan
// `ipaddr          =` tanpa nilai; FreeRADIUS gagal parse dan daemon TIDAK
// NAIK — yang memutus SELURUH instance di mesin itu, bukan satu mitra.
func TestDynamicClientNet_SpasiTakBocorKeTemplate(t *testing.T) {
	t.Setenv("RM_API_TOKEN", "devtoken")
	t.Setenv("RM_API_DYNAMIC_CLIENT_NET", "   ")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.DynamicClientNet != "0.0.0.0/0" {
		t.Errorf("spasi tidak dinormalkan: %q", c.DynamicClientNet)
	}

	t.Setenv("RM_API_DYNAMIC_CLIENT_NET", "  172.31.199.0/24  ")
	c, err = Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.DynamicClientNet != "172.31.199.0/24" {
		t.Errorf("spasi di sekitar CIDR tidak dibuang: %q", c.DynamicClientNet)
	}
}
