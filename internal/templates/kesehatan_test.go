package templates

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func renderKesehatan(t *testing.T, net, secret string) string {
	t.Helper()
	var b bytes.Buffer
	v := Vars{
		InstanceName: "radius_test1", DBHost: "127.0.0.1", DBPort: 3306,
		DBName: "radius_test1", DBUser: "radius_test1", DBPass: "x",
		AuthPort: 14368, AcctPort: 14369, CoAPort: 16368, InnerPort: 19368,
		DynamicClientNet: net, HealthSecret: secret,
	}
	if err := Render(&b, "virtual_server", v); err != nil {
		t.Fatalf("render: %v", err)
	}
	return b.String()
}

const secretUji = "Sehat0123456789abcdefABCDEF0123"

// Desain (tinjauan 17 Nov 2026, MGR-1/MGR-2; teruji di freeradius-server
// 3.2.10 + MariaDB): client kesehatan = jaringan DINAMIS 127.0.0.0/8 di
// daftar per-socket, dibuat dynhealth_<nama> HANYA untuk paket ke port auth,
// dan diproses netovas_health_<nama> yang SELALU menolak — bukan virtual
// server instance (yang punya pap, post-auth radpostauth, dan accounting).
func TestKesehatan_ClientJaringanLoopbackDiDaftarPerSocket(t *testing.T) {
	s := renderKesehatan(t, "0.0.0.0/0", secretUji)
	i := strings.Index(s, "clients dynclist_radius_test1 {")
	j := strings.Index(s, "server radius_test1 {")
	if i < 0 || j < 0 {
		t.Fatalf("struktur tak dikenal:\n%s", s)
	}
	daftar := s[i:j]
	for _, wajib := range []string{
		"client dynhealth_radius_test1 {",
		"ipaddr          = 127.0.0.0/8",
		"dynamic_clients = dynhealth_radius_test1",
	} {
		if !strings.Contains(daftar, wajib) {
			t.Errorf("daftar dynclist tak memuat %q:\n%s", wajib, daftar)
		}
	}
	// Secret TIDAK di daftar client (hanya di server dynhealth).
	if strings.Contains(daftar, secretUji) {
		t.Errorf("secret bocor ke daftar client:\n%s", daftar)
	}
	// Client NAS dinamis tetap ada dan TETAP yang pertama.
	if k := strings.Index(daftar, "client dyn_radius_test1 {"); k < 0 || k > strings.Index(daftar, "client dynhealth_radius_test1 {") {
		t.Errorf("client NAS dinamis hilang / tak lagi pertama:\n%s", daftar)
	}
}

// MGR-1: net sempit (tak memuat 127.0.0.1) dulu membuat cabang kesehatan
// tak pernah terpicu → 503 palsu → ERP memblokir instance sehat. Kini client
// 127.0.0.0/8 dipancarkan apa pun lebar net-nya.
func TestKesehatan_NetSempitTetapPunyaClientLoopback(t *testing.T) {
	for _, net := range []string{"172.16.0.0/12", "172.31.199.0/24", "10.0.0.0/8"} {
		s := renderKesehatan(t, net, secretUji)
		if !strings.Contains(s, "ipaddr          = 127.0.0.0/8") || !strings.Contains(s, "server dynhealth_radius_test1 {") {
			t.Errorf("net %s: client kesehatan tak dipancarkan", net)
		}
	}
}

// MGR-2: client hanya DIBUAT untuk paket dari 127.0.0.1 ke port AUTH, dan
// diproses server pemeriksa — bukan virtual server instance.
func TestKesehatan_ClientHanyaDariLoopbackKePortAuth(t *testing.T) {
	s := renderKesehatan(t, "0.0.0.0/0", secretUji)
	i := strings.Index(s, "server dynhealth_radius_test1 {")
	j := strings.Index(s, "server netovas_health_radius_test1 {")
	if i < 0 || j < 0 || j < i {
		t.Fatalf("server dynhealth/netovas_health tak ada:\n%s", s)
	}
	dyn := s[i:j]
	for _, wajib := range []string{
		"if ((&Packet-Src-IP-Address == 127.0.0.1) && (&Packet-Dst-Port == 14368)) {",
		`&FreeRADIUS-Client-Secret         = "` + secretUji + `"`,
		`&FreeRADIUS-Client-Virtual-Server = "netovas_health_radius_test1"`,
		"&FreeRADIUS-Client-Require-MA     = yes",
		"reject",
	} {
		if !strings.Contains(dyn, wajib) {
			t.Errorf("dynhealth tak memuat %q:\n%s", wajib, dyn)
		}
	}
	if strings.Contains(dyn, `Virtual-Server = "radius_test1"`) {
		t.Errorf("client kesehatan diproses virtual server INSTANCE (pap/post-auth/accounting):\n%s", dyn)
	}
}

// MGR-2: server pemeriksa SELALU menolak, tak punya pap/chap (tak bisa
// menebak sandi), tak punya post-auth (radpostauth tak ditulis), dan menolak
// accounting/CoA (tak bisa menyuntik Accounting-Start/Stop).
func TestKesehatan_ServerPemeriksaSelaluMenolak(t *testing.T) {
	s := renderKesehatan(t, "0.0.0.0/0", secretUji)
	i := strings.Index(s, "server netovas_health_radius_test1 {")
	j := strings.LastIndex(s, "# <<< netovas-health")
	if i < 0 || j < i {
		t.Fatalf("server netovas_health tak ada:\n%s", s)
	}
	sv := s[i:j]
	for _, wajib := range []string{"sql_radius_test1 {", "fail = 1", `"netovas-health: sql gagal"`, "preacct {\n        reject", "accounting {\n        reject", "recv-coa {\n        reject"} {
		if !strings.Contains(sv, wajib) {
			t.Errorf("server pemeriksa tak memuat %q:\n%s", wajib, sv)
		}
	}
	for _, larang := range []string{"pap", "chap", "post-auth", "Post-Auth-Type", "eap_"} {
		if strings.Contains(sv, larang) {
			t.Errorf("server pemeriksa memuat %q:\n%s", larang, sv)
		}
	}
	// Penanda dipakai `radius-manager.sh aktifkan-kesehatan` — tepat satu pasang tiap blok.
	for _, p := range []string{"# >>> netovas-health-client", "# <<< netovas-health-client"} {
		if strings.Count(s, p) != 1 {
			t.Errorf("penanda %q harus tepat satu", p)
		}
	}
	if n := strings.Count(s, "# >>> netovas-health\n"); n != 1 {
		t.Errorf("penanda server netovas-health = %d, mau 1", n)
	}
	// Cabang lama di dynclients (diproses virtual server instance) TIDAK kembali.
	k := strings.Index(s, "server dynclients_radius_test1 {")
	m := strings.Index(s, "# >>> netovas-health\n")
	if k < 0 || m < k || strings.Contains(s[k:m], "127.0.0.1") {
		t.Errorf("dynclients_<nama> masih memuat cabang loopback")
	}
}

// JANGAN kembali ke client STATIS 127.0.0.1. Teruji di freeradius-server
// 3.2.10: client statis di daftar dynclist + satu baris `nas` ber-nasname
// 127.0.0.1 (client SQL masuk daftar yang sama) = "Failed to add duplicate
// client" → FreeRADIUS tidak start → SEMUA instance di mesin itu mati.
func TestKesehatan_BukanClientStatis(t *testing.T) {
	s := renderKesehatan(t, "0.0.0.0/0", secretUji)
	if regexp.MustCompile(`(?m)^[^#\n]*ipaddr\s*=\s*127\.0\.0\.1`).MatchString(s) {
		t.Errorf("client statis 127.0.0.1 dipancarkan:\n%s", s)
	}
}

func TestKesehatanDidukung(t *testing.T) {
	for net, mau := range map[string]bool{
		"":                false,
		"  ":              false,
		"0.0.0.0/0":       true,
		"172.16.0.0/12":   true,
		"127.0.0.0/8":     false, // prefiks sama dengan client kesehatan = duplicate client
		" 127.0.0.0/8 ":   false,
		"127.1.2.3/8":     false, // kanonik 127.0.0.0/8
		"127.0.0.0/16":    true,
		"172.31.199.0/24": true,
	} {
		if got := KesehatanDidukung(net); got != mau {
			t.Errorf("KesehatanDidukung(%q) = %v, mau %v", net, got, mau)
		}
	}
}

// Tanpa secret: tak ada sisa apa pun — config persis seperti sebelum fitur ini.
func TestKesehatan_TanpaSecretTakMengubahApaPun(t *testing.T) {
	if a, b := renderKesehatan(t, "0.0.0.0/0", ""), render(t, "0.0.0.0/0"); a != b {
		t.Fatal("render tanpa HealthSecret berbeda dari render lama")
	}
	if s := renderKesehatan(t, "0.0.0.0/0", ""); strings.Contains(s, "netovas-health") || strings.Contains(s, "127.0.0.1") {
		t.Errorf("sisa blok kesehatan tanpa secret:\n%s", s)
	}
}

// NAS dinamis mati → virtual server dynclients tidak ada → client kesehatan
// juga tidak, MESKI secret terisi. Tak ada tempat aman lain untuknya: di
// daftar global, 127.0.0.1 instance kedua menduplikasi milik instance pertama.
func TestKesehatan_NASDinamisMatiTakDipancarkan(t *testing.T) {
	s := renderKesehatan(t, "", secretUji)
	if strings.Contains(s, secretUji) || strings.Contains(s, "netovas-health") {
		t.Errorf("blok kesehatan dipancarkan tanpa NAS dinamis:\n%s", s)
	}
	if s != render(t, "") {
		t.Error("NAS dinamis mati + secret harus identik dengan render lama")
	}
}

func TestHealthSecretValid(t *testing.T) {
	for s, mau := range map[string]bool{
		secretUji:               true,
		"abcdefghijklmnop":      true,
		"":                      false,
		"pendek":                false,
		`abcdefghijklmnop"`:     false,
		"abcdefghijklmnop\nx":   false,
		"abcdefghijklmnop q":    false,
		strings.Repeat("a", 65): false,
		"abcdefghijklmnop\\":    false,
	} {
		if got := HealthSecretValid(s); got != mau {
			t.Errorf("HealthSecretValid(%q) = %v, mau %v", s, got, mau)
		}
	}
}

func TestAlamatKesehatan(t *testing.T) {
	if got := AlamatKesehatan(14368); got != "127.0.0.1:14368" {
		t.Fatalf("AlamatKesehatan = %q", got)
	}
}
