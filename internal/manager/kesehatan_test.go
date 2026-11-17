package manager

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Pemeriksaan kesehatan FreeRADIUS (GET /api/v1/radius/health freeradius-api).
//
// Latar: 17 Nov 2026, pindah RADIUS LintasHome PALI. ERP memverifikasi server
// baru LEWAT freeradius-api (API sehat) lalu menyatakan "Siap dipasang" walau
// FreeRADIUS-nya MATI — router dialihkan, semua login "radius timeout". Supaya
// ERP bisa berhenti SEBELUM menyentuh router, tiap instance harus bisa ditanya
// "FreeRADIUS-mu menjawab?" — dan jawaban itu hanya benar bila secret di
// .env API dan di config FreeRADIUS instance yang SAMA identik.

func barisEnv(env, kunci string) (string, bool) {
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(kunci) + `=(.*)$`).FindStringSubmatch(env)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func berkasBerakhiran(t *testing.T, files map[string][]byte, akhiran string) string {
	t.Helper()
	for k, v := range files {
		if strings.HasSuffix(k, akhiran) {
			return string(v)
		}
	}
	t.Fatalf("berkas berakhiran %s tidak ditulis; files=%v", akhiran, keys(files))
	return ""
}

// Instance baru (NAS dinamis aktif — bawaan RM-API) mendapat alamat + secret
// kesehatan di .env, dan secret yang SAMA persis di config FreeRADIUS-nya.
func TestCreate_KesehatanSecretSamaDiConfigDanEnv(t *testing.T) {
	i, _, _, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()
	i.cfg.DynamicClientNet = "0.0.0.0/0"
	expectCreateDB(mock)

	resp, err := i.CreateInstance(context.Background(), createReq("mitra_x"))
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	env := string(fs.Files[filepath.Join(i.cfg.APIDirBase, "mitra_x-api", ".env")])
	site := berkasBerakhiran(t, fs.Files, "/sites-available/mitra_x")

	addr, ok := barisEnv(env, "RADIUS_HEALTH_ADDR")
	if !ok {
		t.Fatalf("RADIUS_HEALTH_ADDR tidak ada di .env:\n%s", env)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", resp.Ports.Auth); addr != want {
		t.Errorf("RADIUS_HEALTH_ADDR = %q, mau %q (port AUTH instance, lewat loopback)", addr, want)
	}
	secret, ok := barisEnv(env, "RADIUS_HEALTH_SECRET")
	if !ok || len(secret) < 16 {
		t.Fatalf("RADIUS_HEALTH_SECRET kosong/pendek di .env: %q", secret)
	}

	m := regexp.MustCompile(`&FreeRADIUS-Client-Secret\s+= "([^"%]+)"`).FindAllStringSubmatch(site, -1)
	if len(m) != 1 {
		t.Fatalf("mau tepat 1 secret kesehatan literal di config, dapat %d:\n%s", len(m), site)
	}
	if m[0][1] != secret {
		t.Errorf("secret di config (%q) ≠ secret di .env (%q) — API akan menjawab 503 untuk FreeRADIUS yang sehat", m[0][1], secret)
	}
}

// Dua instance di mesin yang sama tidak boleh berbagi secret kesehatan.
func TestCreate_KesehatanSecretAcakPerInstance(t *testing.T) {
	ambil := func() string {
		i, _, _, fs, mock, cleanup := newCreateTestImpl(t)
		defer cleanup()
		i.cfg.DynamicClientNet = "0.0.0.0/0"
		expectCreateDB(mock)
		if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err != nil {
			t.Fatalf("CreateInstance: %v", err)
		}
		s, _ := barisEnv(string(fs.Files[filepath.Join(i.cfg.APIDirBase, "mitra_x-api", ".env")]), "RADIUS_HEALTH_SECRET")
		return s
	}
	if a, b := ambil(), ambil(); a == "" || a == b {
		t.Fatalf("secret kesehatan tidak acak: %q vs %q", a, b)
	}
}

// NAS dinamis MATI → tak ada client kesehatan di config, jadi .env JANGAN
// memuat secret: API harus menjawab 501 ("belum dikonfigurasi" = ERP
// menganggap tak didukung), bukan 503 palsu ("FreeRADIUS mati") yang akan
// menghentikan pindah RADIUS untuk server yang sehat.
func TestCreate_NASDinamisMati_TanpaKesehatan(t *testing.T) {
	i, _, _, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()
	i.cfg.DynamicClientNet = ""
	expectCreateDB(mock)

	if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	env := string(fs.Files[filepath.Join(i.cfg.APIDirBase, "mitra_x-api", ".env")])
	for _, k := range []string{"RADIUS_HEALTH_ADDR", "RADIUS_HEALTH_SECRET"} {
		if _, ada := barisEnv(env, k); ada {
			t.Errorf("%s ditulis padahal config FreeRADIUS tak punya client kesehatan:\n%s", k, env)
		}
	}
	if site := berkasBerakhiran(t, fs.Files, "/sites-available/mitra_x"); strings.Contains(site, "netovas-health") {
		t.Errorf("blok kesehatan dipancarkan padahal NAS dinamis mati:\n%s", site)
	}
}

// MGR-1 (tinjauan 17 Nov 2026): RM_API_DYNAMIC_CLIENT_NET sempit (tak memuat
// 127.0.0.1) dulu tetap menulis secret ke .env, padahal paket loopback tak
// pernah memicu client dinamis → 503 palsu → ERP memblokir instance sehat.
// Kini client kesehatan berprefiks 127.0.0.0/8 sendiri, jadi net sempit
// tetap mendapat pemeriksaan yang BEKERJA; net persis 127.0.0.0/8 (prefiks
// bentrok = duplicate client = mesin mati) → tanpa kesehatan (501).
func TestCreate_NetSempit_KesehatanTetapBekerja(t *testing.T) {
	for _, kasus := range []struct {
		net       string
		kesehatan bool
	}{
		{"172.16.0.0/12", true},
		{"172.31.199.0/24", true},
		{"127.0.0.0/8", false},
	} {
		t.Run(kasus.net, func(t *testing.T) {
			i, _, _, fs, mock, cleanup := newCreateTestImpl(t)
			defer cleanup()
			i.cfg.DynamicClientNet = kasus.net
			expectCreateDB(mock)
			if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err != nil {
				t.Fatalf("CreateInstance: %v", err)
			}
			env := string(fs.Files[filepath.Join(i.cfg.APIDirBase, "mitra_x-api", ".env")])
			site := berkasBerakhiran(t, fs.Files, "/sites-available/mitra_x")
			_, adaEnv := barisEnv(env, "RADIUS_HEALTH_SECRET")
			adaClient := strings.Contains(site, "client dynhealth_mitra_x {")
			if adaEnv != kasus.kesehatan || adaClient != kasus.kesehatan {
				t.Fatalf("net %s: secret di .env=%v, client 127.0.0.0/8 di config=%v; mau keduanya %v",
					kasus.net, adaEnv, adaClient, kasus.kesehatan)
			}
		})
	}
}

// Penjaga kelas bug insiden 17 Nov (setup.sh freeradius-api menulis mati
// login/radius_db "radius" sementara .env memakai DB lain → FreeRADIUS
// "Access denied ... to database 'radius'"): user, sandi, dan nama DB yang
// dipakai modul SQL FreeRADIUS HARUS identik dengan yang dipakai API dan
// yang benar-benar dibuat di MariaDB.
func TestCreate_KredensialDBSamaDiModulSQLDanEnv(t *testing.T) {
	i, _, _, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()
	expectCreateDB(mock) // membuat DB `mitra_x` dan user 'mitra_x'@'localhost'

	resp, err := i.CreateInstance(context.Background(), createReq("mitra_x"))
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB/user yang dibuat bukan mitra_x: %v", err)
	}
	sqlMod := berkasBerakhiran(t, fs.Files, "/mods-available/sql_mitra_x")
	env := string(fs.Files[filepath.Join(i.cfg.APIDirBase, "mitra_x-api", ".env")])

	nilaiMod := func(k string) string {
		m := regexp.MustCompile(`(?m)^\s*` + k + `\s*=\s*"([^"]*)"`).FindStringSubmatch(sqlMod)
		if m == nil {
			t.Fatalf("%s tidak ada di modul SQL:\n%s", k, sqlMod)
		}
		return m[1]
	}
	nilaiEnv := func(k string) string {
		v, ok := barisEnv(env, k)
		if !ok {
			t.Fatalf("%s tidak ada di .env", k)
		}
		return v
	}
	cek := []struct{ mod, env, mau string }{
		{"login", "DB_USER", "mitra_x"},
		{"radius_db", "DB_NAME", "mitra_x"},
		{"password", "DB_PASSWORD", resp.Database.Password},
	}
	for _, c := range cek {
		if m, e := nilaiMod(c.mod), nilaiEnv(c.env); m != c.mau || e != c.mau {
			t.Errorf("%s modul=%q, %s .env=%q, mau keduanya %q", c.mod, m, c.env, e, c.mau)
		}
	}
}

// Addr dan Secret datang berdua atau tidak sama sekali; secret aneh ditolak
// sebelum sampai ke berkas yang dimuat SATU daemon untuk semua instance.
func TestRenderBlokKesehatanEnv_Penjaga(t *testing.T) {
	baik := "AbCdEfGhIjKlMnOpQrStUvWxYz012345"
	kasus := []struct {
		nama, addr, secret string
		galat              bool
	}{
		{"keduanya kosong", "", "", false},
		{"keduanya ada", "127.0.0.1:11000", baik, false},
		{"addr saja", "127.0.0.1:11000", "", true},
		{"secret saja", "", baik, true},
		{"secret berkutip", "127.0.0.1:11000", `abcdefghijklmnop"q`, true},
		{"secret pendek", "127.0.0.1:11000", "pendek", true},
	}
	for _, k := range kasus {
		p := paramsUji()
		p.RadiusHealthAddr, p.RadiusHealthSecret = k.addr, k.secret
		_, err := renderAPIEnvFileGo(p)
		if (err != nil) != k.galat {
			t.Errorf("%s: galat=%v, mau galat=%v", k.nama, err, k.galat)
		}
	}
}
