//go:build windows

// internal/config/secure_windows.go
//
// Windows-only secure config loader/saver using DPAPI.
//
// Changes vs legacy:
// - Store config.enc under %AppData%\Feralthedogg\AppleMusicRPC (per-user, default)
//   and optionally %ProgramData%\Feralthedogg\AppleMusicRPC (per-machine).
// - Auto-migrate from legacy "config.enc/config.json next to the EXE" when found.
// - Keep the old public function names (LoadNearExeSecure/SaveNearExeSecure)
//   so call-sites do NOT need to change.

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	companyName  = "Feralthedogg"
	productName  = "AppleMusicRPC"
	encFileName  = "config.enc"
	plainFile    = "config.json"
	dpapiEntropy = "amrpc.applemusic.v1"

	// DPAPI flag: bind encryption to the machine (decryptable by any user on that machine).
	// Only use this if you intentionally want a machine-wide config in ProgramData.
	cryptProtectLocalMachine = 0x4
)

// Scope selects where the encrypted config is stored.
type Scope int

const (
	// ScopeUser stores under %AppData% (roaming) for the current user.
	ScopeUser Scope = iota

	// ScopeMachine stores under %ProgramData% for the machine (requires appropriate ACL).
	ScopeMachine
)

// dataBlob matches the Windows DATA_BLOB structure used by DPAPI.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	crypt32              = windows.NewLazySystemDLL("crypt32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procCryptProtectData = crypt32.NewProc("CryptProtectData")
	procCryptUnprotect   = crypt32.NewProc("CryptUnprotectData")
	procLocalFree        = kernel32.NewProc("LocalFree")
)

// dpapiProtect encrypts bytes using Windows DPAPI.
// If localMachine is true, the ciphertext can be decrypted by any user on the same machine.
// If false (default), it is tied to the current user context.
func dpapiProtect(in []byte, localMachine bool) ([]byte, error) {
	if len(in) == 0 {
		return []byte{}, nil
	}

	inBlob := dataBlob{cbData: uint32(len(in)), pbData: &in[0]}
	ent := []byte(dpapiEntropy)
	entBlob := dataBlob{cbData: uint32(len(ent)), pbData: &ent[0]}

	var outBlob dataBlob

	var flags uintptr
	if localMachine {
		flags = cryptProtectLocalMachine
	}

	r1, _, e1 := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&inBlob)),  // pDataIn
		0,                                 // szDataDescr
		uintptr(unsafe.Pointer(&entBlob)), // pOptionalEntropy
		0,                                 // pvReserved
		0,                                 // pPromptStruct
		flags,                             // dwFlags
		uintptr(unsafe.Pointer(&outBlob)), // pDataOut
	)
	if r1 == 0 {
		return nil, fmt.Errorf("CryptProtectData failed: %v", e1)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))

	out := unsafe.Slice(outBlob.pbData, outBlob.cbData)
	cp := make([]byte, len(out))
	copy(cp, out)
	return cp, nil
}

// dpapiUnprotect decrypts DPAPI-protected bytes.
func dpapiUnprotect(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return []byte{}, nil
	}

	inBlob := dataBlob{cbData: uint32(len(in)), pbData: &in[0]}
	ent := []byte(dpapiEntropy)
	entBlob := dataBlob{cbData: uint32(len(ent)), pbData: &ent[0]}

	var outBlob dataBlob

	r1, _, e1 := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(&inBlob)),  // pDataIn
		0,                                 // ppszDataDescr
		uintptr(unsafe.Pointer(&entBlob)), // pOptionalEntropy
		0,                                 // pvReserved
		0,                                 // pPromptStruct
		0,                                 // dwFlags
		uintptr(unsafe.Pointer(&outBlob)), // pDataOut
	)
	if r1 == 0 {
		return nil, fmt.Errorf("CryptUnprotectData failed: %v", e1)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))

	out := unsafe.Slice(outBlob.pbData, outBlob.cbData)
	cp := make([]byte, len(out))
	copy(cp, out)
	return cp, nil
}

// userBaseDir returns %AppData% (roaming) reliably.
func userBaseDir() (string, error) {
	// os.UserConfigDir() maps to %AppData% on Windows (Roaming).
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return d, nil
	}
	// Fallback to APPDATA env var.
	if d := os.Getenv("APPDATA"); d != "" {
		return d, nil
	}
	return "", errors.New("cannot resolve %AppData%")
}

// machineBaseDir returns %ProgramData%.
func machineBaseDir() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}

// userConfigDir returns "%AppData%\Feralthedogg\AppleMusicRPC".
func userConfigDir() (string, error) {
	base, err := userBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, companyName, productName), nil
}

// machineConfigDir returns "%ProgramData%\Feralthedogg\AppleMusicRPC".
func machineConfigDir() string {
	return filepath.Join(machineBaseDir(), companyName, productName)
}

// legacyNearExeDir returns the directory containing the running executable.
func legacyNearExeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// ensureDir creates the directory if needed.
func ensureDir(p string) error {
	return os.MkdirAll(p, 0o700) // Windows ignores POSIX perms; still fine.
}

// loadFromEncPath reads+decrypts config.enc at path.
func loadFromEncPath(encPath string) (*Config, error) {
	b, err := os.ReadFile(encPath)
	if err != nil {
		return nil, err
	}
	raw, err := dpapiUnprotect(b)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// saveToEncPath encrypts+atomically writes config.enc at path.
// localMachine controls DPAPI scope; see dpapiProtect().
func saveToEncPath(encPath string, cfg *Config, localMachine bool) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	enc, err := dpapiProtect(raw, localMachine)
	if err != nil {
		return err
	}

	if err := ensureDir(filepath.Dir(encPath)); err != nil {
		return err
	}

	tmp := encPath + ".tmp"
	if err := os.WriteFile(tmp, enc, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, encPath)
}

// LoadSecure loads configuration with the given scope preference.
// Search order:
// 1) Preferred scope path (user or machine)
// 2) The other scope path (optional fallback)
// 3) Legacy next-to-EXE config.enc (migrate to preferred scope if readable)
// 4) Legacy next-to-EXE config.json (import -> encrypt -> delete plaintext, best-effort)
func LoadSecure(prefer Scope) (*Config, error) {
	// Build candidate paths.
	var preferEnc, otherEnc string
	var preferIsMachine bool

	switch prefer {
	case ScopeMachine:
		preferEnc = filepath.Join(machineConfigDir(), encFileName)
		otherDir, err := userConfigDir()
		if err == nil {
			otherEnc = filepath.Join(otherDir, encFileName)
		}
		preferIsMachine = true
	default:
		userDir, err := userConfigDir()
		if err == nil {
			preferEnc = filepath.Join(userDir, encFileName)
		}
		otherEnc = filepath.Join(machineConfigDir(), encFileName)
		preferIsMachine = false
	}

	// 1) Preferred scope
	if preferEnc != "" {
		if cfg, err := loadFromEncPath(preferEnc); err == nil {
			return cfg, nil
		}
	}

	// 2) Other scope fallback
	if otherEnc != "" {
		if cfg, err := loadFromEncPath(otherEnc); err == nil {
			return cfg, nil
		}
	}

	// 3/4) Legacy migration from next-to-EXE
	legacyDir, err := legacyNearExeDir()
	if err == nil && legacyDir != "" {
		legacyEnc := filepath.Join(legacyDir, encFileName)
		legacyJson := filepath.Join(legacyDir, plainFile)

		// 3) legacy config.enc
		if cfg, err := loadFromEncPath(legacyEnc); err == nil {
			// Migrate to preferred scope.
			if preferEnc != "" {
				_ = saveToEncPath(preferEnc, cfg, preferIsMachine)
				// Best-effort remove legacy file (may fail if installed under Program Files).
				_ = os.Remove(legacyEnc)
			}
			return cfg, nil
		}

		// 4) legacy config.json
		if b, err := os.ReadFile(legacyJson); err == nil {
			var cfg Config
			if err := json.Unmarshal(b, &cfg); err != nil {
				return nil, err
			}

			// Encrypt into preferred scope.
			if preferEnc == "" {
				return nil, errors.New("cannot resolve config storage directory")
			}
			if err := saveToEncPath(preferEnc, &cfg, preferIsMachine); err != nil {
				return nil, err
			}

			// Best-effort delete plaintext.
			_ = os.Remove(legacyJson)

			return &cfg, nil
		}
	}

	return nil, errors.New("config not found (no config.enc in AppData/ProgramData, and no legacy config.json/config.enc)")
}

// SaveSecure saves configuration to the selected scope.
func SaveSecure(scope Scope, cfg *Config) error {
	switch scope {
	case ScopeMachine:
		encPath := filepath.Join(machineConfigDir(), encFileName)
		return saveToEncPath(encPath, cfg, true /*localMachine*/)
	default:
		dir, err := userConfigDir()
		if err != nil {
			return err
		}
		encPath := filepath.Join(dir, encFileName)
		return saveToEncPath(encPath, cfg, false /*per-user*/)
	}
}

// LoadNearExeSecure is kept for backward compatibility.
// It now loads from per-user %AppData% by default (with migration support).
func LoadNearExeSecure() (*Config, error) {
	return LoadSecure(ScopeUser)
}

// SaveNearExeSecure is kept for backward compatibility.
// It now saves into per-user %AppData% by default.
func SaveNearExeSecure(cfg *Config) error {
	return SaveSecure(ScopeUser, cfg)
}
