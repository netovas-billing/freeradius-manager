package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPortRegistry_AllocateInRange(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	port, err := r.AllocateAuthPort("inst1")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if port < 10000 || port > 59000 {
		t.Fatalf("port %d outside expected range [10000,59000]", port)
	}
}

func TestPortRegistry_RegisterAllocatesQuad(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	port, err := r.AllocateAuthPort("inst1")
	if err != nil {
		t.Fatal(err)
	}

	used, err := r.UsedPorts()
	if err != nil {
		t.Fatal(err)
	}
	want := []int{port, port + 1, port + 2000, port + 5000}
	for _, w := range want {
		if !used[w] {
			t.Fatalf("expected port %d to be registered after AllocateAuthPort, used=%v", w, used)
		}
	}
}

func TestPortRegistry_AvoidsExistingQuadConflict(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	// Force a quad allocated for inst1.
	first, err := r.AllocateAuthPort("inst1")
	if err != nil {
		t.Fatal(err)
	}

	// Allocate many more — none should ever collide on the quad of inst1.
	for i := 0; i < 50; i++ {
		p, err := r.AllocateAuthPort("inst" + string(rune('a'+i)))
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if p == first || p == first+1 || p == first+2000 || p == first+5000 ||
			p+1 == first || p+2000 == first || p+5000 == first {
			t.Fatalf("collision: new=%d first=%d", p, first)
		}
	}
}

func TestPortRegistry_UnregisterRemovesQuad(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	port, _ := r.AllocateAuthPort("inst1")
	if err := r.Unregister("inst1"); err != nil {
		t.Fatal(err)
	}
	used, _ := r.UsedPorts()
	for _, p := range []int{port, port + 1, port + 2000, port + 5000} {
		if used[p] {
			t.Fatalf("port %d still registered after Unregister", p)
		}
	}
}

func TestPortRegistry_AllocateAPIPortSequential(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))
	r.APIPortStart = 8100

	p1, err := r.AllocateAPIPort("inst1")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != 8100 {
		t.Fatalf("first API port: got %d want 8100", p1)
	}
	p2, err := r.AllocateAPIPort("inst2")
	if err != nil {
		t.Fatal(err)
	}
	if p2 != 8101 {
		t.Fatalf("second API port: got %d want 8101", p2)
	}
}

func TestPortRegistry_ConcurrentAllocationsAreUnique(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	const N = 20
	var wg sync.WaitGroup
	results := make(chan int, N)
	errs := make(chan error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := r.AllocateAuthPort("inst" + string(rune('a'+i)))
			if err != nil {
				errs <- err
				return
			}
			results <- p
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	for e := range errs {
		t.Fatal(e)
	}

	seen := map[int]bool{}
	for p := range results {
		// Each allocation registers a quad — none can collide with any other quad.
		quad := []int{p, p + 1, p + 2000, p + 5000}
		for _, q := range quad {
			if seen[q] {
				t.Fatalf("concurrent allocation produced collision at %d", q)
			}
			seen[q] = true
		}
	}
}

// Blok port API harus bisa digeser per-mesin (RM_API_API_PORT_START): dua VM
// RADIUS di bawah satu concentrator tidak boleh memakai blok yang sama, aturan
// DSTNAT-nya bertabrakan dan salah satunya gagal diam-diam.
func TestNewPortRegistryWithAPIStart(t *testing.T) {
	cases := []struct {
		name  string
		start int
		want  int
	}{
		{"blok kustom dipakai apa adanya", 8300, 8300},
		{"nol (env tak diisi) jatuh ke bawaan", 0, DefaultAPIPortStart},
		{"di bawah batas jatuh ke bawaan", MinAPIPortStart - 1, DefaultAPIPortStart},
		{"di atas batas jatuh ke bawaan", MaxAPIPortStart + 1, DefaultAPIPortStart},
		{"negatif jatuh ke bawaan", -1, DefaultAPIPortStart},
		{"batas bawah masih sah", MinAPIPortStart, MinAPIPortStart},
		{"batas atas masih sah", MaxAPIPortStart, MaxAPIPortStart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := NewPortRegistryWithAPIStart(filepath.Join(dir, "ports.txt"), tc.start)
			if r.APIPortStart != tc.want {
				t.Fatalf("APIPortStart: got %d want %d", r.APIPortStart, tc.want)
			}
			// Rentang port UDP RADIUS tidak boleh ikut bergeser.
			if r.AuthPortMin != 10000 || r.AuthPortMax != 59000 {
				t.Fatalf("rentang auth berubah: [%d,%d]", r.AuthPortMin, r.AuthPortMax)
			}
		})
	}
}

func TestPortRegistry_AllocateAPIPortHonorsCustomBlock(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistryWithAPIStart(filepath.Join(dir, "ports.txt"), 8300)

	p1, err := r.AllocateAPIPort("inst1")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != 8300 {
		t.Fatalf("first API port: got %d want 8300", p1)
	}
	p2, err := r.AllocateAPIPort("inst2")
	if err != nil {
		t.Fatal(err)
	}
	if p2 != 8301 {
		t.Fatalf("second API port: got %d want 8301", p2)
	}
}

// Alokasi port API TIDAK BOLEH keluar dari blok milik mesin ini. Port di luar
// blok tidak ikut di-DSTNAT concentrator: instance-nya lahir "berhasil" tapi
// tak pernah bisa dihubungi backend — dan kalau blok VM tetangga terlewati,
// URL-nya malah mendarat di VM yang keliru. Dua-duanya gagal senyap.
func TestPortRegistry_APIPortNeverLeavesBlock(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistryWithAPIStart(filepath.Join(dir, "ports.txt"), 20100)
	r.APIBlockWidth = 3

	for i := 0; i < 3; i++ {
		p, err := r.AllocateAPIPort("inst" + string(rune('a'+i)))
		if err != nil {
			t.Fatalf("alokasi ke-%d: %v", i, err)
		}
		if p < 20100 || p > 20102 {
			t.Fatalf("port %d keluar dari blok [20100,20103)", p)
		}
	}
	// Blok habis: harus GALAT, bukan diam-diam memakai 20103.
	p, err := r.AllocateAPIPort("instd")
	if err == nil {
		t.Fatalf("blok habis tapi tetap mengalokasikan port %d di luar blok", p)
	}
	if !errors.Is(err, ErrPortExhausted) {
		t.Fatalf("galat harus membungkus ErrPortExhausted (dipetakan HTTP layer), got %v", err)
	}
	if !strings.Contains(err.Error(), "20100") || !strings.Contains(err.Error(), "20103") {
		t.Fatalf("galat harus menyebut rentang bloknya, got %v", err)
	}
}

// CapacityMax lebih sempit dari lebar blok: ERP menghitung rentang DSTNAT dari
// CapacityMax, jadi port ke-(CapacityMax+1) pun sudah di luar yang di-NAT.
func TestPortRegistry_APIPortRespectsCapacityMax(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistryWithAPIStart(filepath.Join(dir, "ports.txt"), 20100)
	r.CapacityMax = 2

	for i := 0; i < 2; i++ {
		if _, err := r.AllocateAPIPort("inst" + string(rune('a'+i))); err != nil {
			t.Fatalf("alokasi ke-%d: %v", i, err)
		}
	}
	if p, err := r.AllocateAPIPort("instc"); err == nil {
		t.Fatalf("CapacityMax=2 tapi alokasi ke-3 lolos di port %d", p)
	}
}

// Pagar terakhir: apa pun setelannya, penelusuran tidak boleh menerbitkan
// nomor port yang mustahil (> 65535).
func TestPortRegistry_APIPortNeverExceedsMaxPort(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistryWithAPIStart(filepath.Join(dir, "ports.txt"), MaxAPIPortStart)
	r.APIPortStart = 65534 // di luar klem konstruktor, disetel langsung
	r.APIBlockWidth = 1000

	if end := r.APIPortEnd(); end != MaxPortNumber+1 {
		t.Fatalf("batas atas: got %d want %d", end, MaxPortNumber+1)
	}
	for i := 0; i < 2; i++ {
		p, err := r.AllocateAPIPort("inst" + string(rune('a'+i)))
		if err != nil {
			t.Fatalf("alokasi ke-%d: %v", i, err)
		}
		if p > MaxPortNumber {
			t.Fatalf("port %d melewati %d", p, MaxPortNumber)
		}
	}
	if p, err := r.AllocateAPIPort("instc"); err == nil {
		t.Fatalf("melewati batas port sah: %d", p)
	}
}

// Blok kanonik dari ERP (base 20000 + k*1000 → api_port_start = base+100) harus
// lolos apa adanya, termasuk di batas atas 64000 yang masih sah.
func TestPortRegistry_APIPortBlockCanonicalAndBoundary(t *testing.T) {
	cases := []struct {
		name  string
		start int
		want  int
	}{
		{"VM-1 (base 20000)", 20100, 20100},
		{"VM-2 (base 21000)", 21100, 21100},
		{"batas atas 64000", MaxAPIPortStart, MaxAPIPortStart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := NewPortRegistryWithAPIStart(filepath.Join(dir, "ports.txt"), tc.start)
			if r.APIBlockWidth != DefaultAPIBlockWidth {
				t.Fatalf("lebar blok bawaan: got %d want %d", r.APIBlockWidth, DefaultAPIBlockWidth)
			}
			p, err := r.AllocateAPIPort("inst1")
			if err != nil {
				t.Fatal(err)
			}
			if p != tc.want {
				t.Fatalf("port pertama: got %d want %d", p, tc.want)
			}
			if end := r.APIPortEnd(); end > MaxPortNumber+1 {
				t.Fatalf("batas atas melewati port sah: %d", end)
			}
		})
	}
}

// Skenario temuan pagar-salah-lebar: blok VM ke-k = [base, base+999] dan
// api_port_start = base+100, jadi rentang instance yang sah cuma 900 port.
// Dulu lebar 1000 dihitung DARI api_port_start, sehingga pagarnya membentang
// sampai base+1099: dengan 20100..20999 terpakai dan CapacityMax=1000,
// AllocateAPIPort mengembalikan 21000 — itu RM_API_LISTEN milik VM slot
// BERIKUTNYA, bukan port mesin ini. Instance yang lahir di sana tidak
// di-DSTNAT ke mesin ini dan permintaan backend mendarat di VM yang keliru,
// sementara provisioning tetap dilaporkan BERHASIL.
func TestPortRegistry_APIPortNeverEntersNeighborBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.txt")

	// 20100..20999 terpakai penuh (900 port = seluruh rentang instance).
	var b strings.Builder
	for p := 20100; p <= 20999; p++ {
		fmt.Fprintf(&b, "%d # inst%d api\n", p, p)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewPortRegistryWithAPIStart(path, 20100)
	r.CapacityMax = 1000 // lebih lebar dari rentang; tidak boleh melonggarkan pagar

	p, err := r.AllocateAPIPort("instbaru")
	if err == nil {
		t.Fatalf("rentang penuh tapi mengalokasikan port %d (21000 = RM-API VM tetangga)", p)
	}
	if !errors.Is(err, ErrPortExhausted) {
		t.Fatalf("galat harus membungkus ErrPortExhausted, got %v", err)
	}
	if end := r.APIPortEnd(); end != 21000 {
		t.Fatalf("batas atas (eksklusif): got %d want 21000", end)
	}
}

// Lebar rentang instance dihitung DARI api_port_start dan harus 900:
// [base+100, base+999]. 100 port pertama blok dipakai RM-API (base =
// RM_API_LISTEN) dan kontrol. Angka 1000 di sini berarti pagar merambah ke
// blok VM tetangga — gagal senyap, lihat uji di atas.
func TestDefaultAPIBlockWidthIs900(t *testing.T) {
	if DefaultAPIBlockWidth != 900 {
		t.Fatalf("DefaultAPIBlockWidth: got %d want 900 (base+100..base+999)", DefaultAPIBlockWidth)
	}
	dir := t.TempDir()
	r := NewPortRegistryWithAPIStart(filepath.Join(dir, "ports.txt"), 20100)
	if end := r.APIPortEnd(); end != 21000 {
		t.Fatalf("pagar blok kanonik: got [20100,%d) want [20100,21000)", end)
	}
}

// Pagar = api_port_start + min(lebar, CapacityMax). Kedua arah harus benar,
// karena ERP menghitung rentang DSTNAT dari CapacityMax.
func TestPortRegistry_APIPortEndTakesNarrowest(t *testing.T) {
	cases := []struct {
		name     string
		start    int
		width    int
		capacity int
		want     int
	}{
		{"capacity lebih sempit", 20100, 900, 50, 20150},
		{"capacity lebih lebar (tak melonggarkan)", 20100, 900, 1000, 21000},
		{"capacity 0 = tidak membatasi", 20100, 900, 0, 21000},
		{"lebar bawaan", 21100, DefaultAPIBlockWidth, 900, 22000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := NewPortRegistryWithAPIStart(filepath.Join(dir, "ports.txt"), tc.start)
			r.APIBlockWidth = tc.width
			r.CapacityMax = tc.capacity
			if end := r.APIPortEnd(); end != tc.want {
				t.Fatalf("APIPortEnd: got %d want %d", end, tc.want)
			}
		})
	}
}
