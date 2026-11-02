package manager

import (
	"context"
	"strings"
	"testing"

	"github.com/netovas-billing/freeradius-manager/internal/system"
)

func bootstrapGo() (*FreeRADIUSAPIBootstrap, *system.MockGit, *system.MockGo, *system.MockFilesystem) {
	g := system.NewMockGit()
	gо := system.NewMockGo()
	fs := system.NewMockFilesystem()
	b := &FreeRADIUSAPIBootstrap{
		GoRepoURL:     "https://github.com/netovas-billing/freeradius-api",
		GoTemplateDir: "/var/lib/radius-manager-api/freeradius-api-go-template",
		Git:           g,
		Go:            gо,
		FS:            fs,
	}
	// Prasyarat: skrip pemeliharaan HARUS ada di template, kalau tidak
	// setupInstanceGo menolak (instance tanpa backup DB = tak boleh terjadi).
	for _, skrip := range skripPemeliharaan {
		fs.PresetExists[b.GoTemplateDir+"/"+skrip] = true
	}
	return b, g, gо, fs
}

func paramsUji() SetupInstanceParams {
	return SetupInstanceParams{
		APIDir:       "/root/mitra_x-api",
		InstanceName: "mitra_x",
		DBHost:       "127.0.0.1",
		DBPort:       3306,
		DBName:       "radius_mitra_x",
		DBUser:       "radius_mitra_x",
		DBPass:       "rahasia",
		SwaggerUser:  "admin",
		SwaggerPass:  "sw-pass",
		APIPort:      20100,
	}
}

// Build + salin BINER saja, tanpa venv, tanpa pip, tanpa pohon sumber.
func TestRuntimeGo_BinerBukanPohonSumber(t *testing.T) {
	b, _, gо, fs := bootstrapGo()
	if err := b.SetupInstance(context.Background(), paramsUji()); err != nil {
		t.Fatal(err)
	}
	for k := range fs.Files {
		if strings.Contains(k, "/venv") {
			t.Fatalf("ada berkas venv tertulis: %s", k)
		}
	}
	binTemplate := "/var/lib/radius-manager-api/freeradius-api-go-template/api/freeradius-api"
	if gо.Built[binTemplate] == "" {
		t.Fatalf("biner tidak dibangun di template; Built=%v", gо.Built)
	}
	if _, ada := fs.Files["/root/mitra_x-api/freeradius-api"]; !ada {
		t.Fatalf("biner tidak disalin ke direktori instance; Files=%v", kunci(fs.Files))
	}
	// Pohon sumber TIDAK boleh disalin: ~34 MB vendor x 43 instance tanpa guna.
	for _, c := range fs.Calls {
		if c.Method == "CopyDir" {
			t.Fatalf("CopyDir dipanggil pada runtime go (%v) — seharusnya hanya biner", c.Args)
		}
	}
}

// .env bentuk Go harus memuat PORT dan kredensial Basic dengan NAMA BAKU.
// Tanpa PORT, semua instance mencoba bind 8000 dan yang kedua gagal.
// Tanpa BASIC_AUTH_*, aplikasi membalas 401 untuk SETIAP panggilan ERP.
func TestRuntimeGo_EnvMemuatPortDanBasicAuth(t *testing.T) {
	b, _, _, fs := bootstrapGo()
	if err := b.SetupInstance(context.Background(), paramsUji()); err != nil {
		t.Fatal(err)
	}
	env := string(fs.Files["/root/mitra_x-api/.env"])
	for _, wajib := range []string{
		"PORT=20100",
		"BASIC_AUTH_USER=admin",
		"BASIC_AUTH_PASSWORD=sw-pass",
		"DB_NAME=radius_mitra_x",
		"DB_TIMEZONE=Asia/Jakarta",
		"RATE_LIMIT_MAX=",
	} {
		if !strings.Contains(env, wajib) {
			t.Fatalf("%q tidak ada di .env:\n%s", wajib, env)
		}
	}
	// API_KEY harus terisi acak, bukan nilai contoh.
	if strings.Contains(env, "change-me") {
		t.Fatalf("API_KEY memakai nilai contoh:\n%s", env)
	}
}

// APIPort wajib: kalau lupa diteruskan, JANGAN diam-diam jatuh ke 8000.
func TestRuntimeGo_APIPortWajib(t *testing.T) {
	b, _, _, _ := bootstrapGo()
	p := paramsUji()
	p.APIPort = 0
	err := b.SetupInstance(context.Background(), p)
	if err == nil {
		t.Fatal("APIPort=0 diterima — semua instance akan bind port yang sama")
	}
	if !strings.Contains(err.Error(), "APIPort") {
		t.Fatalf("pesan galat tidak menyebut APIPort: %v", err)
	}
}

// PENJAGA TERPENTING (tetap berlaku meski runtime Python sudah dibuang):
// jangan pernah mengubah instance PYTHON LAMA yang masih berjalan di VM menjadi
// Go secara implisit. Unit lamanya menunjuk venv/bin/uvicorn
// dan tidak ditulis ulang oleh jalur mana pun, jadi menimpa .env-nya akan
// mematikan instance yang sedang melayani pelanggan tanpa ada yang memperbaikinya.
func TestRuntimeGo_MenolakMengubahInstancePython(t *testing.T) {
	b, _, gо, fs := bootstrapGo()
	fs.PresetExists["/root/mitra_x-api"] = true
	fs.PresetExists["/root/mitra_x-api/venv"] = true

	err := b.SetupInstance(context.Background(), paramsUji())
	if err == nil {
		t.Fatal("instance Python yang sudah ada diubah ke go tanpa penolakan")
	}
	if !strings.Contains(err.Error(), "PYTHON") {
		t.Fatalf("pesan galat tidak menjelaskan sebabnya: %v", err)
	}
	if len(gо.Calls) != 0 {
		t.Fatalf("build dijalankan padahal seharusnya ditolak lebih dulu: %v", gо.Calls)
	}
	if _, ada := fs.Files["/root/mitra_x-api/.env"]; ada {
		t.Fatal(".env instance Python DITIMPA — ini yang harus dicegah")
	}
}

// Tanpa RM_API_GO_REPO harus gagal KERAS dengan pesan yang menyebut
// variabelnya, dan git tidak boleh dipanggil sama sekali.
func TestRuntimeGo_TanpaRepoGagalKeras(t *testing.T) {
	b, g, _, _ := bootstrapGo()
	b.GoRepoURL = ""
	err := b.EnsureTemplate(context.Background())
	if err == nil {
		t.Fatal("GoRepoURL kosong diterima")
	}
	if !strings.Contains(err.Error(), "RM_API_GO_REPO") {
		t.Fatalf("pesan galat tidak menyebut variabelnya: %v", err)
	}
	if len(g.Calls) != 0 {
		t.Fatalf("git dipanggil padahal repo kosong: %v", g.Calls)
	}
}

// Ref yang dipaku harus benar-benar dipakai saat clone, dan template yang
// dipaku TIDAK boleh di-pull — kalau di-pull, "versi terpasang" jadi
// pertanyaan terbuka lagi, padahal itu justru yang dipaku.
func TestRuntimeGo_RefDipakuDanTidakDiPull(t *testing.T) {
	b, g, _, fs := bootstrapGo()
	b.GoRef = "v0.3.0"

	if err := b.EnsureTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.Refs[b.GoTemplateDir] != "v0.3.0" {
		t.Fatalf("ref tidak dipaku saat clone: %v", g.Refs)
	}

	// Template sudah ada -> tidak boleh Pull karena ref dipaku.
	g2 := system.NewMockGit()
	b.Git = g2
	fs.PresetExists[b.GoTemplateDir] = true
	if err := b.EnsureTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range g2.Calls {
		if c.Method == "Pull" {
			t.Fatal("template yang dipaku di-Pull — versinya jadi tak tentu lagi")
		}
	}
}

func kunci(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Unit systemd runtime Go: TIDAK boleh menyebut uvicorn/venv, harus menunjuk
// binernya, dan harus mempertahankan hal-hal yang dipakai jalur lain —
// WorkingDirectory (tempat .env dibaca) dan SyslogIdentifier.
func TestUnitSystemdGo(t *testing.T) {
	i := &impl{}
	unit := i.renderAPIServiceUnitGo("mitra_x", "/root/mitra_x-api")

	for _, terlarang := range []string{"uvicorn", "venv", "main:app", "--port"} {
		if strings.Contains(unit, terlarang) {
			t.Fatalf("unit Go masih menyebut %q:\n%s", terlarang, unit)
		}
	}
	for _, wajib := range []string{
		"ExecStart=/root/mitra_x-api/freeradius-api",
		"WorkingDirectory=/root/mitra_x-api",
		"SyslogIdentifier=mitra_x-api",
		"Restart=always",
	} {
		if !strings.Contains(unit, wajib) {
			t.Fatalf("unit Go kehilangan %q:\n%s", wajib, unit)
		}
	}
}

// Skrip pemeliharaan absen di template → create GAGAL, dan tidak ada .env yang
// tertulis. Ini penjaga terhadap kegagalan yang paling mahal di jalur ini:
// instance yang jalan tanpa backup basis data, gagal senyap di timer, dan baru
// diketahui pada saat backup-nya dibutuhkan.
func TestRuntimeGo_SkripPemeliharaanWajibAdaDiTemplate(t *testing.T) {
	for _, hilang := range skripPemeliharaan {
		b, _, _, fs := bootstrapGo()
		delete(fs.PresetExists, b.GoTemplateDir+"/"+hilang)

		err := b.SetupInstance(context.Background(), paramsUji())
		if err == nil {
			t.Fatalf("%s hilang tapi create diterima", hilang)
		}
		if !strings.Contains(err.Error(), hilang) {
			t.Fatalf("pesan galat tidak menyebut %s: %v", hilang, err)
		}
		if !strings.Contains(err.Error(), "backup") {
			t.Fatalf("pesan galat tidak menjelaskan akibatnya: %v", err)
		}
		if _, ada := fs.Files["/root/mitra_x-api/.env"]; ada {
			t.Fatalf(".env tertulis padahal %s hilang — instance separuh jadi", hilang)
		}
	}
}

// Dan saat ada, keduanya harus BENAR-BENAR mendarat di direktori instance:
// timer menunjuk ke sana, dan PatchScripts menambal kredensialnya di tempat.
func TestRuntimeGo_SkripPemeliharaanDisalinKeInstance(t *testing.T) {
	b, _, _, fs := bootstrapGo()
	if err := b.SetupInstance(context.Background(), paramsUji()); err != nil {
		t.Fatal(err)
	}
	for _, skrip := range skripPemeliharaan {
		if _, ada := fs.Files["/root/mitra_x-api/"+skrip]; !ada {
			t.Fatalf("%s tidak disalin ke direktori instance; Files=%v", skrip, kunci(fs.Files))
		}
	}
}
