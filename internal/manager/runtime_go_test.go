package manager

import (
	"context"
	"strings"
	"testing"

	"github.com/netovas-billing/freeradius-manager/internal/system"
)

func bootstrapGo() (*FreeRADIUSAPIBootstrap, *system.MockGit, *system.MockPython, *system.MockGo, *system.MockFilesystem) {
	g := system.NewMockGit()
	py := system.NewMockPython()
	gо := system.NewMockGo()
	fs := system.NewMockFilesystem()
	b := &FreeRADIUSAPIBootstrap{
		RepoURL:       "https://github.com/heirro/freeradius-api",
		TemplateDir:   "/var/lib/radius-manager-api/freeradius-api-template",
		Runtime:       RuntimeGo,
		GoRepoURL:     "https://github.com/netovas-billing/freeradius-api",
		GoTemplateDir: "/var/lib/radius-manager-api/freeradius-api-go-template",
		Git:           g,
		Python:        py,
		Go:            gо,
		FS:            fs,
	}
	return b, g, py, gо, fs
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

// BAWAAN TIDAK MENGUBAH APA PUN.
//
// Ini kendala keras pemilik: instance yang sudah berjalan dengan Python tidak
// boleh terpengaruh. Tanpa menyetel RM_API_RUNTIME, jalurnya harus tetap
// Python — venv dibuat, pip dipanggil, .env bentuk Python.
func TestBawaanTetapPython(t *testing.T) {
	for _, nilai := range []string{"", "python", "PYTHON", "  python  "} {
		b, _, py, gо, fs := bootstrapGo()
		b.Runtime = nilai
		if b.PakaiGo() {
			t.Fatalf("Runtime=%q seharusnya BUKAN go", nilai)
		}
		if err := b.SetupInstance(context.Background(), paramsUji()); err != nil {
			t.Fatalf("Runtime=%q: %v", nilai, err)
		}
		if len(py.Calls) == 0 {
			t.Fatalf("Runtime=%q: venv/pip tidak dipanggil — jalur Python tidak dilalui", nilai)
		}
		if len(gо.Calls) != 0 {
			t.Fatalf("Runtime=%q: go build dipanggil padahal runtime python", nilai)
		}
		env := string(fs.Files["/root/mitra_x-api/.env"])
		if !strings.Contains(env, "SWAGGER_USERNAME=admin") {
			t.Fatalf("Runtime=%q: .env bukan bentuk Python:\n%s", nilai, env)
		}
		// Baris PORT= sendiri, bukan substring: DB_PORT=3306 juga memuat "PORT=".
		for _, baris := range strings.Split(env, "\n") {
			if strings.HasPrefix(strings.TrimSpace(baris), "PORT=") {
				t.Fatalf("Runtime=%q: .env Python tidak boleh memuat PORT:\n%s", nilai, env)
			}
		}
	}
}

// Runtime go: build + salin BINER saja, tanpa venv, tanpa pip.
func TestRuntimeGo_BinerBukanPohonSumber(t *testing.T) {
	b, _, py, gо, fs := bootstrapGo()
	if err := b.SetupInstance(context.Background(), paramsUji()); err != nil {
		t.Fatal(err)
	}
	if len(py.Calls) != 0 {
		t.Fatalf("venv/pip dipanggil pada runtime go: %v", py.Calls)
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
	b, _, _, _, fs := bootstrapGo()
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
	b, _, _, _, _ := bootstrapGo()
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

// PENJAGA TERPENTING: jangan pernah mengubah instance PYTHON yang sudah
// berjalan menjadi Go secara implisit. Unit lamanya menunjuk venv/bin/uvicorn
// dan tidak ditulis ulang oleh jalur mana pun, jadi menimpa .env-nya akan
// mematikan instance yang sedang melayani pelanggan tanpa ada yang memperbaikinya.
func TestRuntimeGo_MenolakMengubahInstancePython(t *testing.T) {
	b, _, _, gо, fs := bootstrapGo()
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

// runtime=go tanpa RM_API_GO_REPO harus gagal KERAS, bukan jatuh ke repo Python:
// memasang aplikasi yang berbeda dari yang diminta lebih buruk daripada gagal.
func TestRuntimeGo_TanpaRepoGagalKeras(t *testing.T) {
	b, g, _, _, _ := bootstrapGo()
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
	b, g, _, _, fs := bootstrapGo()
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

// Template Python dan Go harus DIPISAH: repo berbeda, jadi satu direktori
// berarti yang satu menimpa checkout yang lain.
func TestTemplateDirTerpisahPerRuntime(t *testing.T) {
	b, _, _, _, _ := bootstrapGo()
	if b.templateDir() != b.GoTemplateDir {
		t.Fatalf("runtime go memakai template Python: %s", b.templateDir())
	}
	b.Runtime = RuntimePython
	if b.templateDir() != b.TemplateDir {
		t.Fatalf("runtime python memakai template Go: %s", b.templateDir())
	}
	if b.TemplateDir == b.GoTemplateDir {
		t.Fatal("kedua template menunjuk direktori yang SAMA")
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

// Unit Python TIDAK BOLEH berubah: itu yang dipakai instance yang sudah jalan.
func TestUnitSystemdPythonTidakBerubah(t *testing.T) {
	i := &impl{}
	unit := i.renderAPIServiceUnit("mitra_x", "/root/mitra_x-api", 20100)
	for _, wajib := range []string{
		"ExecStart=/root/mitra_x-api/venv/bin/uvicorn main:app --host 0.0.0.0 --port 20100 --workers 4",
		"WorkingDirectory=/root/mitra_x-api",
		"SyslogIdentifier=mitra_x-api",
	} {
		if !strings.Contains(unit, wajib) {
			t.Fatalf("unit Python berubah — instance yang sudah jalan memakai bentuk ini.\nhilang: %q\n%s", wajib, unit)
		}
	}
}
