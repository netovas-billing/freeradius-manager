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

// Client kesehatan adalah cabang PERTAMA virtual server dynclients_<nama>:
// sumber 127.0.0.1 → secret kesehatan, diproses virtual server INSTANCE INI
// (jadi melewati sql_<nama>), lalu `return` supaya tak jatuh ke pencarian
// tabel `nas`.
func TestKesehatan_CabangDinamisPertama(t *testing.T) {
	s := renderKesehatan(t, "0.0.0.0/0", secretUji)

	iDyn := strings.Index(s, "server dynclients_radius_test1 {")
	if iDyn < 0 {
		t.Fatalf("virtual server dynclients tidak ada:\n%s", s)
	}
	dyn := s[iDyn:]
	iSehat := strings.Index(dyn, "if (&Packet-Src-IP-Address == 127.0.0.1) {")
	iSQL := strings.Index(dyn, `if ("%{sql_radius_test1: SELECT nasname`)
	if iSehat < 0 || iSQL < 0 || iSehat > iSQL {
		t.Fatalf("cabang kesehatan harus ada dan SEBELUM pencarian nas (sehat=%d sql=%d):\n%s", iSehat, iSQL, dyn)
	}
	cabang := dyn[iSehat:iSQL]
	for _, wajib := range []string{
		`&FreeRADIUS-Client-Secret         = "` + secretUji + `"`,
		`&FreeRADIUS-Client-Virtual-Server = "radius_test1"`,
		`&FreeRADIUS-Client-IP-Address     = 127.0.0.1`,
		"return",
	} {
		if !strings.Contains(cabang, wajib) {
			t.Errorf("cabang kesehatan tak memuat %q:\n%s", wajib, cabang)
		}
	}
	// Penanda dipakai `radius-manager.sh aktifkan-kesehatan` untuk mengenali
	// instance yang sudah punya blok ini — tepat satu pasang.
	if strings.Count(s, "# >>> netovas-health") != 1 || strings.Count(s, "# <<< netovas-health") != 1 {
		t.Errorf("penanda netovas-health harus tepat satu pasang:\n%s", s)
	}
}

// JANGAN kembali ke client STATIS 127.0.0.1. Teruji di freeradius-server
// 3.2.10: client statis di daftar dynclist + satu baris `nas` ber-nasname
// 127.0.0.1 (client SQL masuk daftar yang sama) = "Failed to add duplicate
// client" → FreeRADIUS tidak start → SEMUA instance di mesin itu mati.
func TestKesehatan_BukanClientStatis(t *testing.T) {
	s := renderKesehatan(t, "0.0.0.0/0", secretUji)
	i := strings.Index(s, "clients dynclist_radius_test1 {")
	j := strings.Index(s, "server radius_test1 {")
	if i < 0 || j < 0 {
		t.Fatalf("struktur tak dikenal:\n%s", s)
	}
	daftar := s[i:j]
	if strings.Contains(daftar, "127.0.0.1") || strings.Contains(daftar, secretUji) {
		t.Errorf("client kesehatan statis muncul di daftar dynclist:\n%s", daftar)
	}
	if regexp.MustCompile(`(?m)^[^#\n]*ipaddr\s*=\s*127\.0\.0\.1`).MatchString(s) {
		t.Errorf("client statis 127.0.0.1 dipancarkan:\n%s", s)
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
