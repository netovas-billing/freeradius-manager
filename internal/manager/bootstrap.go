package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/netovas-billing/freeradius-manager/internal/system"
)

// S3Config carries the S3 backup destination passed to the
// autobackups3.sh credential patcher. Zero-value means the
// autobackups3 timer is not installed (and any pre-existing job is
// removed). Mirrors the constants from radius-manager.sh:
//
//	S3_REMOTE        ljns3
//	S3_BUCKET        backup-db
//	S3_BACKUP_ROOT   radiusdb       (per-instance suffix appended)
type S3Config struct {
	Remote     string
	Bucket     string
	BackupRoot string
}

// IsZero reports whether the S3Config is unconfigured. Treats Remote
// and Bucket as the required pair; BackupRoot can default to "radiusdb"
// without enabling backups. This matches the config-side contract:
// empty RM_API_S3_REMOTE means "no backups".
func (s S3Config) IsZero() bool {
	return s.Remote == "" || s.Bucket == ""
}

// FreeRADIUSAPIBootstrap memasang freeradius-api (runtime Go) per instance
// dengan strategi template-sekali + salin-per-instance (RM-Q opsi B):
//
//   - EnsureTemplate meng-clone repo Go ke GoTemplateDir sekali, lalu hanya
//     Pull (atau tidak sama sekali bila SkipPull / ref dipaku).
//   - SetupInstance membangun biner di template, menyalin biner + skrip
//     pemeliharaan ke APIDir, dan menulis `.env`.
//
// Runtime Python (clone repo Python, venv, pip, unit uvicorn) sudah DIBUANG
// (pemilik, 30 Okt 2026). Instance Python LAMA yang masih berjalan tidak
// disentuh jalur ini — penjaga venv/ di setupInstanceGo menolak menimpanya.
//
// Semua operasi bisa di-mock lewat system.Git, system.GoToolchain, dan
// system.Filesystem.
type FreeRADIUSAPIBootstrap struct {
	SkipPull bool

	GoRepoURL     string
	GoRef         string // tag/branch/commit; kosong = branch bawaan (tidak dipaku)
	GoTemplateDir string

	Git system.Git
	Go  system.GoToolchain
	FS  system.Filesystem

	once sync.Mutex
}

const (
	// namaBinerGo — nama biner di direktori instance. Unit systemd menunjuk
	// nama ini, jadi mengubahnya memutus instance yang sudah ada.
	namaBinerGo = "freeradius-api"

	// subdirPaketGo — paket main repo Go ada di api/, bukan di akar.
	subdirPaketGo = "api"
)

// skripPemeliharaan — skrip yang HARUS ikut mendarat di direktori instance.
//
// Timer pemeliharaan menunjuk keduanya DI DALAM direktori instance, dan
// PatchScripts menambal kredensialnya di tempat. Hanya biner yang disalin dari
// template, jadi keduanya harus disebut eksplisit — kalau tidak, timernya
// ENOENT dan instance jalan tanpa pembersih zombie dan tanpa backup basis data.
var skripPemeliharaan = []string{"autoclearzombie.sh", "autobackups3.sh"}

// EnsureTemplate memastikan GoTemplateDir ada dan mutakhir.
// Aman dipanggil bersamaan.
func (b *FreeRADIUSAPIBootstrap) EnsureTemplate(ctx context.Context) error {
	b.once.Lock()
	defer b.once.Unlock()

	dir := b.GoTemplateDir
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("RM_API_GO_TEMPLATE_DIR kosong")
	}
	repo := b.GoRepoURL
	if strings.TrimSpace(repo) == "" {
		// Gagal-keras dengan pesan yang menyebut variabelnya: create yang gagal
		// jelas jauh lebih murah daripada instance tanpa aplikasi.
		return fmt.Errorf("RM_API_GO_REPO kosong — tak ada repo freeradius-api untuk di-clone")
	}

	exists, err := b.FS.Exists(ctx, dir)
	if err != nil {
		return fmt.Errorf("probe template dir: %w", err)
	}
	if !exists {
		if err := b.Git.CloneRef(ctx, repo, b.GoRef, dir); err != nil {
			return fmt.Errorf("git clone template: %w", err)
		}
		return nil
	}
	if b.SkipPull {
		return nil
	}
	if strings.TrimSpace(b.GoRef) != "" {
		// Checkout yang DIPAKU tidak boleh di-pull: `pull --ff-only` pada HEAD
		// terlepas bukan sekadar gagal, ia membuat "versi terpasang" jadi
		// pertanyaan terbuka lagi — padahal justru itu yang dipaku.
		return nil
	}
	if err := b.Git.Pull(ctx, dir); err != nil {
		return fmt.Errorf("git pull template: %w", err)
	}
	return nil
}

// SetupInstanceParams collects the parameters SetupInstance needs.
type SetupInstanceParams struct {
	APIDir       string // e.g., /root/mitra_x-api
	InstanceName string
	DBHost       string
	DBPort       int
	DBName       string
	DBUser       string
	DBPass       string
	SwaggerUser  string // default "admin"
	SwaggerPass  string
	// APIPort — wajib untuk runtime go: port di-hardcode ":8000" di aplikasi
	// sebelum ini, dan satu VM menjalankan banyak instance.
	APIPort int
}

// SetupInstance memasang direktori API satu instance (runtime Go).
func (b *FreeRADIUSAPIBootstrap) SetupInstance(ctx context.Context, p SetupInstanceParams) error {
	if err := validateIdentifier(p.InstanceName); err != nil {
		return err
	}
	if p.APIDir == "" {
		return fmt.Errorf("APIDir is required")
	}

	exists, err := b.FS.Exists(ctx, p.APIDir)
	if err != nil {
		return fmt.Errorf("probe api dir: %w", err)
	}
	return b.setupInstanceGo(ctx, p, exists)
}

// setupInstanceGo memasang instance dengan runtime Go.
//
// Yang mendarat di direktori instance hanya BINER, skrip pemeliharaan, dan
// `.env`, bukan seluruh pohon sumber. Menyalin sumbernya per instance berarti
// ~34 MB vendor dikali jumlah instance (maksimum 43 per VM) tanpa satu pun
// manfaat — binernya statis dan sama untuk semuanya.
func (b *FreeRADIUSAPIBootstrap) setupInstanceGo(ctx context.Context, p SetupInstanceParams, apiDirAda bool) error {
	if b.Go == nil {
		return fmt.Errorf("toolchain Go tidak di-wire")
	}
	if strings.TrimSpace(b.GoTemplateDir) == "" {
		return fmt.Errorf("GoTemplateDir kosong")
	}

	// PENJAGA: jangan pernah mengubah instance PYTHON lama menjadi Go secara
	// implisit. Runtime Python memang sudah dibuang, tapi instance Python yang
	// dibuat sebelumnya MASIH berjalan di VM: direktorinya memuat venv/ dan
	// dilayani unit yang menunjuk venv/bin/uvicorn. Menimpa .env dengan bentuk Go
	// akan mematikan instance yang sedang melayani pelanggan, dan unit lamanya
	// tidak ikut ditulis ulang oleh jalur mana pun sehingga tak ada yang
	// memperbaikinya.
	if apiDirAda {
		venvAda, _ := b.FS.Exists(ctx, filepath.Join(p.APIDir, "venv"))
		if venvAda {
			return fmt.Errorf("direktori %s adalah instance PYTHON lama (ada venv/) — "+
				"menolak menimpanya dengan runtime go; hapus dulu lewat DELETE instance "+
				"(atau `radius-manager.sh delete`), atau pakai nama lain", p.APIDir)
		}
	}

	// Build di TEMPLATE, bukan per instance: `go build` memakai cache, jadi
	// pemanggilan berikutnya murah, dan binernya dijamin cocok dengan template
	// yang sedang aktif.
	pkgDir := filepath.Join(b.GoTemplateDir, subdirPaketGo)
	binTemplate := filepath.Join(pkgDir, namaBinerGo)
	if err := b.Go.Build(ctx, pkgDir, binTemplate); err != nil {
		return fmt.Errorf("build biner go: %w", err)
	}

	binInstance := filepath.Join(p.APIDir, namaBinerGo)
	if err := b.FS.CopyFile(ctx, binTemplate, binInstance, 0o755); err != nil {
		return fmt.Errorf("salin biner ke %s: %w", binInstance, err)
	}

	// Skrip pemeliharaan ikut, mode 0700 seperti jalur bash (yang menulisnya
	// dengan WriteFile 0o700). Absen di template = gagal KERAS, bukan dilewati:
	// instance tanpa backup basis data yang tak disadari jauh lebih mahal
	// daripada create yang gagal dengan pesan jelas.
	for _, skrip := range skripPemeliharaan {
		asal := filepath.Join(b.GoTemplateDir, skrip)
		ada, _ := b.FS.Exists(ctx, asal)
		if !ada {
			return fmt.Errorf("skrip pemeliharaan %s tidak ada di template %s — "+
				"instance akan jalan tanpa backup basis data", skrip, b.GoTemplateDir)
		}
		if err := b.FS.CopyFile(ctx, asal, filepath.Join(p.APIDir, skrip), 0o700); err != nil {
			return fmt.Errorf("salin %s: %w", skrip, err)
		}
	}

	envContent, err := renderAPIEnvFileGo(p)
	if err != nil {
		return err
	}
	envPath := filepath.Join(p.APIDir, ".env")
	if err := b.FS.WriteFile(ctx, envPath, []byte(envContent), 0o600); err != nil {
		return err
	}
	return nil
}

// batasRateGo — batas rate limit yang ditulis ke .env instance Go.
//
// Bawaan aplikasinya 120 permintaan/60 detik dengan SATU ember untuk seluruh
// backend (kuncinya nama kredensial, bukan IP). Backend menyusuri daftar
// FreeRADIUS 1.000 baris per permintaan dengan batas 200 halaman, jadi satu
// penyapuan tabel besar saja sudah bisa melewati 120 dalam satu menit, dan
// tiap 429 berarti sinkronisasi ERP yang terputus di tengah jalan.
//
// API ini privat per mitra dan hanya dijangkau backend lewat dst-nat, jadi
// pembatas di sini bukan lapisan keamanan; ia hanya penahan loop liar.
const batasRateGo = 6000

func renderAPIEnvFileGo(p SetupInstanceParams) (string, error) {
	swUser := p.SwaggerUser
	if swUser == "" {
		swUser = "admin"
	}
	if p.APIPort <= 0 {
		return "", fmt.Errorf("APIPort wajib untuk runtime go (dapat %d)", p.APIPort)
	}
	// API_KEY acak, BUKAN dikosongkan: kalau kosong, aplikasi mematikan jalur
	// X-API-Key (itu perilaku gagal-tertutup yang benar), tapi memberi kunci
	// sungguhan berarti ada jalan masuk kedua yang tidak memakai kredensial
	// Basic milik ERP — berguna untuk diagnosa tanpa membocorkan kredensial itu.
	apiKey, err := GeneratePassword(32)
	if err != nil {
		return "", fmt.Errorf("bangkitkan API_KEY: %w", err)
	}
	return fmt.Sprintf(`# freeradius-api (runtime Go) — instance %s
# Dihasilkan radius-manager-api. Suntingan manual akan tertimpa saat create ulang.

PORT=%d

# Kredensial Basic yang dipakai ERP. Nama SWAGGER_* juga diterima aplikasi
# sebagai alias, tapi nama baku yang ditulis di sini supaya tak ada tebakan.
BASIC_AUTH_USER=%s
BASIC_AUTH_PASSWORD=%s

# Jalan masuk kedua untuk diagnosa. Kosongkan untuk mematikan jalur X-API-Key.
API_KEY=%s

# Database
DB_HOST=%s
DB_PORT=%d
DB_NAME=%s
DB_USER=%s
DB_PASSWORD=%s

# Kolom waktu radacct NAIF dan ditulis dalam WIB. Dibuat EKSPLISIT supaya
# penafsirannya tidak ikut TZ sistem: di VM ber-TZ UTC pergeserannya 7 jam dan
# masuk langsung ke angka tagihan tanpa satu pun galat.
DB_TIMEZONE=Asia/Jakarta

RATE_LIMIT_MAX=%d
RATE_LIMIT_WINDOW_SECONDS=60
`, p.InstanceName, p.APIPort, swUser, p.SwaggerPass, apiKey,
		p.DBHost, p.DBPort, p.DBName, p.DBUser, p.DBPass, batasRateGo), nil
}

// Teardown removes the API directory. Idempotent.
func (b *FreeRADIUSAPIBootstrap) Teardown(ctx context.Context, apiDir string) error {
	return b.FS.RemoveDir(ctx, apiDir)
}

// PatchScripts rewrites the hardcoded `KEY="..."` shell-variable lines
// at the top of the freeradius-api maintenance scripts so the cloned
// template gets per-instance DB credentials and S3 destination baked
// in. Mirrors what radius-manager.sh:setup_api() does with sed(1).
//
// Files handled:
//
//	autoclearzombie.sh — DB_HOST, DB_PORT, DB_USER, DB_PASS, DB_NAME
//	autobackups3.sh   — DB_* (same keys) + REMOTE, BUCKET, BACKUP_PATH
//
// Idempotent: a missing script is silently skipped (treated as a
// template that doesn't ship that maintenance helper). The Maintenance
// interface backend (.service unit) also injects these as environment
// variables; the in-script patching is belt-and-suspenders for older
// freeradius-api revisions that don't honor env overrides.
func (b *FreeRADIUSAPIBootstrap) PatchScripts(ctx context.Context, p SetupInstanceParams, s3 S3Config) error {
	if err := validateIdentifier(p.InstanceName); err != nil {
		return err
	}
	if p.APIDir == "" {
		return fmt.Errorf("APIDir is required")
	}

	dbReplacements := map[string]string{
		"DB_HOST": p.DBHost,
		"DB_PORT": fmt.Sprintf("%d", p.DBPort),
		"DB_USER": p.DBUser,
		"DB_PASS": p.DBPass,
		"DB_NAME": p.DBName,
	}

	// autoclearzombie.sh: DB credentials only.
	if err := b.patchScript(ctx,
		filepath.Join(p.APIDir, "autoclearzombie.sh"),
		dbReplacements,
	); err != nil {
		return fmt.Errorf("patch autoclearzombie.sh: %w", err)
	}

	// autobackups3.sh: DB credentials + S3 destination.
	backupReplacements := make(map[string]string, len(dbReplacements)+3)
	for k, v := range dbReplacements {
		backupReplacements[k] = v
	}
	if !s3.IsZero() {
		backupReplacements["REMOTE"] = s3.Remote
		backupReplacements["BUCKET"] = s3.Bucket
		// BACKUP_PATH = <root>/<instance>  (matches bash setup_api()).
		backupReplacements["BACKUP_PATH"] = path.Join(s3.BackupRoot, p.InstanceName)
	}
	if err := b.patchScript(ctx,
		filepath.Join(p.APIDir, "autobackups3.sh"),
		backupReplacements,
	); err != nil {
		return fmt.Errorf("patch autobackups3.sh: %w", err)
	}
	return nil
}

// patchScript loads scriptPath, rewrites each `^KEY=...$` line whose
// KEY appears in replacements, and writes the file back with mode 0700
// (preserving the executable bit the bash flow sets via chmod +x).
//
// If the script doesn't exist, returns nil (idempotent skip). Any
// other read error is returned wrapped.
func (b *FreeRADIUSAPIBootstrap) patchScript(ctx context.Context, scriptPath string, replacements map[string]string) error {
	content, err := b.FS.ReadFile(ctx, scriptPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", scriptPath, err)
	}
	patched := applyShellVarPatches(content, replacements)
	if err := b.FS.WriteFile(ctx, scriptPath, patched, 0o700); err != nil {
		return fmt.Errorf("write %s: %w", scriptPath, err)
	}
	return nil
}

// applyShellVarPatches rewrites lines matching `^KEY=...$` (anchored at
// start of a line, allowing leading whitespace is intentional) for any
// KEY in replacements. Other lines pass through untouched, including
// comments, blanks, and any in-line definitions where `KEY=` is not at
// the start of the line. This matches sed `-e "s|^KEY=.*|KEY=...|"`.
func applyShellVarPatches(content []byte, replacements map[string]string) []byte {
	if len(replacements) == 0 {
		return content
	}
	keys := make([]string, 0, len(replacements))
	for k := range replacements {
		keys = append(keys, regexp.QuoteMeta(k))
	}
	// Build a single regex `^(KEY1|KEY2|...)=.*$` (multiline) to keep
	// the rewrite a single pass over the file.
	re := regexp.MustCompile(`(?m)^(` + joinAlt(keys) + `)=.*$`)
	return re.ReplaceAllFunc(content, func(line []byte) []byte {
		// Extract the matched KEY (everything before the first '=').
		eq := indexByte(line, '=')
		if eq < 0 {
			return line
		}
		key := string(line[:eq])
		val, ok := replacements[key]
		if !ok {
			return line
		}
		return []byte(key + `="` + val + `"`)
	})
}

// joinAlt joins regex alternatives without depending on strings.Join
// import (kept tiny to avoid bloating the patch surface).
func joinAlt(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "|" + p
	}
	return out
}

// indexByte mirrors bytes.IndexByte without forcing the import here.
func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
