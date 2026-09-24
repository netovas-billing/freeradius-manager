package manager

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// PortRegistry persists port allocations in the same flat-file format
// radius-manager.sh writes to:
//
//   <port> # <admin> <kind>
//
// where kind ∈ {auth, acct, coa, inner, api}.
//
// The bash script and the Go RM-API both flock this file so cross-process
// races are safe (advisory POSIX lock).
type PortRegistry struct {
	Path         string
	AuthPortMin  int
	AuthPortMax  int
	APIPortStart int

	// APIBlockWidth lebar rentang port INSTANCE di mesin ini (bawaan
	// DefaultAPIBlockWidth = 900), dihitung MULAI DARI APIPortStart —
	// bukan dari pangkal blok VM. Alokasi TIDAK BOLEH keluar dari
	// [APIPortStart, APIPortStart+APIBlockWidth): di luar itu portnya tidak
	// ikut di-DSTNAT concentrator, instance-nya lahir "berhasil" tapi tak
	// pernah bisa dihubungi backend — dan karena api_port_start = base+100,
	// 100 port di atas blok sudah MILIK VM TETANGGA (dimulai tepat di
	// RM_API_LISTEN VM itu). Lihat DefaultAPIBlockWidth.
	APIBlockWidth int

	// CapacityMax batas jumlah instance di mesin ini (RM_API_CAPACITY_MAX).
	// Dipakai sebagai pagar tambahan: ERP menghitung rentang DSTNAT dari
	// angka ini, jadi port ke-(CapacityMax+1) pun sudah di luar yang di-NAT.
	// 0 = tidak ikut membatasi (lebar blok saja yang berlaku).
	CapacityMax int

	maxAttempts int
}

// Blok port HTTP freeradius-api (APIPortStart dan seterusnya, naik satu-satu
// per instance).
//
// Kenapa ini harus bisa diatur per-mesin: satu VPN concentrator MikroTik bisa
// menaungi LEBIH DARI SATU VM RADIUS, dan backend menjangkau tiap VM lewat
// DSTNAT di IP publik concentrator. NAT-nya 1:1 (tanpa to-ports) karena URL
// instance yang diterbitkan RM-API sudah memuat nomor portnya sendiri — jadi
// dua VM yang memakai blok yang sama BERTABRAKAN di aturan NAT: hanya satu yang
// terjangkau, yang lain gagal diam-diam (permintaan mendarat di VM yang keliru).
//
// Blok tiap VM dialokasikan ERP dengan skema base = 20000 + k*1000 (blok
// selebar 1000: [base, base+999]), lalu RM_API_LISTEN = base dan
// RM_API_API_PORT_START = base + 100 — jadi rentang yang boleh dipakai
// instance cuma 900 port teratas blok itu, [base+100, base+999]; lihat
// DefaultAPIBlockWidth. Nilainya datang dari skrip menu "Server RADIUS Manager
// -> Setup Script" dan harus cocok dengan aturan DSTNAT di concentrator.
// DefaultAPIPortStart (8100) hanya jaring pengaman saat env tidak diisi.
//
// Catatan: AuthPortMin/AuthPortMax (port UDP RADIUS) TIDAK ikut diatur — port
// itu lewat tunnel langsung, tidak pernah di-NAT, jadi tidak pernah bertabrakan
// antar-VM.
const (
	DefaultAPIPortStart = 8100
	MinAPIPortStart     = 1024
	MaxAPIPortStart     = 64000

	// DefaultAPIBlockWidth adalah lebar rentang port INSTANCE, dihitung DARI
	// api_port_start — bukan lebar blok VM.
	//
	// Blok VM ke-k memang selebar 1000 port: [base, base+999] dengan
	// base = 20000 + k*1000. Tapi 100 port pertama blok itu bukan milik
	// instance: base sendiri adalah RM_API_LISTEN (port RM-API mesin ini) dan
	// sisanya dicadangkan untuk kontrol, sehingga instance baru boleh mulai di
	// api_port_start = base + 100. Rentang yang SAH untuk instance karena itu
	// [base+100, base+999] — 900 port, bukan 1000.
	//
	// Kalau angka ini dibiarkan 1000, pagarnya membentang sampai base+1099 dan
	// 100 port teratasnya MILIK VM TETANGGA, dimulai tepat di RM_API_LISTEN VM
	// itu (base+1000). Instance yang lahir di sana tidak di-DSTNAT ke mesin
	// ini: permintaan backend mendarat di VM yang keliru — atau ditelan RM-API
	// tetangga — sementara provisioning tetap dilaporkan BERHASIL. Gagal
	// senyap, dan jejaknya cuma nomor port yang "kebetulan" bulat.
	DefaultAPIBlockWidth = 900

	// MaxPortNumber port TCP tertinggi yang sah; pagar terakhir supaya
	// penelusuran tidak pernah menerbitkan nomor port yang mustahil.
	MaxPortNumber = 65535
)

// NewPortRegistry memakai blok port API bawaan (DefaultAPIPortStart).
func NewPortRegistry(path string) *PortRegistry {
	return NewPortRegistryWithAPIStart(path, DefaultAPIPortStart)
}

// NewPortRegistryWithAPIStart sama dengan NewPortRegistry, tapi awal blok port
// API ditentukan pemanggil — nilainya dialirkan dari RM_API_API_PORT_START
// lewat internal/config. Nilai di luar [MinAPIPortStart, MaxAPIPortStart]
// dianggap salah ketik dan dikembalikan ke bawaan, supaya mesin tidak
// terlanjur menerbitkan URL instance di port yang mustahil.
func NewPortRegistryWithAPIStart(path string, apiPortStart int) *PortRegistry {
	if apiPortStart < MinAPIPortStart || apiPortStart > MaxAPIPortStart {
		apiPortStart = DefaultAPIPortStart
	}
	return &PortRegistry{
		Path:          path,
		AuthPortMin:   10000,
		AuthPortMax:   59000,
		APIPortStart:  apiPortStart,
		APIBlockWidth: DefaultAPIBlockWidth,
		maxAttempts:   10000,
	}
}

type portEntry struct {
	port  int
	admin string
	kind  string
}

// withLock executes fn while holding an exclusive flock on the registry file.
// The file is created if it does not exist.
func (r *PortRegistry) withLock(fn func(f *os.File) error) error {
	f, err := os.OpenFile(r.Path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open port registry %s: %w", r.Path, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", r.Path, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn(f)
}

func parseRegistry(f *os.File) ([]portEntry, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	var out []portEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Format: "<port> # <admin> <kind>"
		hashIdx := strings.Index(line, "#")
		var portStr, rest string
		if hashIdx == -1 {
			portStr = strings.TrimSpace(line)
		} else {
			portStr = strings.TrimSpace(line[:hashIdx])
			rest = strings.TrimSpace(line[hashIdx+1:])
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		entry := portEntry{port: port}
		if rest != "" {
			parts := strings.Fields(rest)
			if len(parts) >= 1 {
				entry.admin = parts[0]
			}
			if len(parts) >= 2 {
				entry.kind = parts[1]
			}
		}
		out = append(out, entry)
	}
	return out, scanner.Err()
}

func writeRegistry(f *os.File, entries []portEntry) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range entries {
		fmt.Fprintf(w, "%d # %s %s\n", e.port, e.admin, e.kind)
	}
	return w.Flush()
}

// usedPortsMap collects every registered port into a fast lookup set.
func usedPortsMap(entries []portEntry) map[int]bool {
	out := make(map[int]bool, len(entries))
	for _, e := range entries {
		out[e.port] = true
	}
	return out
}

// UsedPorts returns the set of all currently registered ports.
// Useful for tests + diagnostics.
func (r *PortRegistry) UsedPorts() (map[int]bool, error) {
	var out map[int]bool
	err := r.withLock(func(f *os.File) error {
		entries, err := parseRegistry(f)
		if err != nil {
			return err
		}
		out = usedPortsMap(entries)
		return nil
	})
	return out, err
}

// AllocateAuthPort finds a free quad (auth, acct=auth+1, coa=auth+2000,
// inner=auth+5000) and registers all four under the given admin name.
//
// Returns the chosen auth port. Equivalent to bash's
// find_available_port + register_port.
func (r *PortRegistry) AllocateAuthPort(admin string) (int, error) {
	var chosen int
	err := r.withLock(func(f *os.File) error {
		entries, err := parseRegistry(f)
		if err != nil {
			return err
		}
		used := usedPortsMap(entries)

		for attempt := 0; attempt < r.maxAttempts; attempt++ {
			port, err := randPortInRange(r.AuthPortMin, r.AuthPortMax)
			if err != nil {
				return err
			}
			quad := []int{port, port + 1, port + 2000, port + 5000}
			conflict := false
			for _, q := range quad {
				if used[q] {
					conflict = true
					break
				}
			}
			if conflict {
				continue
			}
			// Future-proof: also check actual port liveness.
			if anyListening(quad) {
				continue
			}
			// Register the quad.
			for _, q := range quad {
				kind := portKind(q, port)
				entries = append(entries, portEntry{port: q, admin: admin, kind: kind})
			}
			if err := writeRegistry(f, entries); err != nil {
				return err
			}
			chosen = port
			return nil
		}
		return ErrPortExhausted
	})
	if err != nil {
		return 0, err
	}
	return chosen, nil
}

// APIPortEnd mengembalikan batas ATAS (EKSKLUSIF) penelusuran port API:
//
//	api_port_start + min(APIBlockWidth, CapacityMax), dipotong MaxPortNumber+1
//
// Kenapa harus ada batasnya: tanpa pagar, penelusuran menyusuri ribuan port ke
// atas dan akhirnya menerbitkan instance di port yang TIDAK ikut di-DSTNAT
// concentrator. Provisioning tetap dilaporkan sukses, tapi backend tak pernah
// bisa menghubungi instance itu — dan kalau blok VM tetangga terlewati, URL-nya
// malah mendarat di VM yang keliru. Dua-duanya gagal senyap.
//
// Diekspor karena rentang EFEKTIF inilah yang harus dicatat saat start-up.
// Mencetak lebar mentah (APIBlockWidth) menyesatkan: dengan CapacityMax kecil
// pagarnya jauh lebih sempit, dan operator yang percaya angka mentah akan
// memasang aturan DSTNAT untuk port yang tak akan pernah dialokasikan.
func (r *PortRegistry) APIPortEnd() int {
	width := r.APIBlockWidth
	if width <= 0 {
		width = DefaultAPIBlockWidth
	}
	if r.CapacityMax > 0 && r.CapacityMax < width {
		width = r.CapacityMax
	}
	end := r.APIPortStart + width
	if end > MaxPortNumber+1 {
		end = MaxPortNumber + 1
	}
	return end
}

// AllocateAPIPort returns the next sequential available API port starting
// at APIPortStart. Equivalent to bash's find_available_api_port.
//
// Penelusuran berhenti di apiPortEnd(): blok habis dikembalikan sebagai GALAT
// (membungkus ErrPortExhausted, jadi HTTP layer tetap memetakannya seperti
// biasa), bukan diam-diam memakai port di luar blok yang di-NAT.
func (r *PortRegistry) AllocateAPIPort(admin string) (int, error) {
	var chosen int
	end := r.APIPortEnd()
	err := r.withLock(func(f *os.File) error {
		entries, err := parseRegistry(f)
		if err != nil {
			return err
		}
		used := usedPortsMap(entries)
		for port := r.APIPortStart; port < end; port++ {
			if used[port] {
				continue
			}
			if anyListening([]int{port}) {
				continue
			}
			entries = append(entries, portEntry{port: port, admin: admin, kind: "api"})
			if err := writeRegistry(f, entries); err != nil {
				return err
			}
			chosen = port
			return nil
		}
		return fmt.Errorf("%w: blok port API [%d,%d) habis; lebarkan blok mesin ini "+
			"(RM_API_API_PORT_START / RM_API_CAPACITY_MAX) lewat skrip yang diterbitkan "+
			"menu \"Server RADIUS Manager -> Setup Script\", jangan pakai port di luar blok",
			ErrPortExhausted, r.APIPortStart, end)
	})
	if err != nil {
		return 0, err
	}
	return chosen, nil
}

// Unregister removes every entry whose admin matches name. Idempotent.
func (r *PortRegistry) Unregister(admin string) error {
	return r.withLock(func(f *os.File) error {
		entries, err := parseRegistry(f)
		if err != nil {
			return err
		}
		kept := entries[:0]
		for _, e := range entries {
			if e.admin == admin {
				continue
			}
			kept = append(kept, e)
		}
		return writeRegistry(f, kept)
	})
}

func portKind(q, base int) string {
	switch q - base {
	case 0:
		return "auth"
	case 1:
		return "acct"
	case 2000:
		return "coa"
	case 5000:
		return "inner"
	}
	return "unknown"
}

func randPortInRange(min, max int) (int, error) {
	if max <= min {
		return 0, fmt.Errorf("invalid port range [%d,%d]", min, max)
	}
	span := uint32(max - min + 1)
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	n := binary.BigEndian.Uint32(b[:]) % span
	return int(n) + min, nil
}

// anyListening reports true if at least one port in the slice has a TCP
// or UDP listener bound on localhost. Conservative: any failure is
// treated as "not listening" so we don't block allocation when net APIs
// are unavailable (e.g., sandboxed test runners).
//
// In v0.1.0 we no-op so unit tests are deterministic. The real probe is
// added in Phase 3 alongside the test endpoint.
func anyListening(_ []int) bool { return false }
