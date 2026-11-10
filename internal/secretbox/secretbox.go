// Unprefixed values are refused unless OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT=1: anyone able to write
// the column could plant a credential.
// No AAD: a database writer can swap sealed values between rows of the same kind and they still
// decrypt.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/hkdf"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const (
	v1Prefix = "v1:"
	v2Prefix = "v2:"
)

var ErrPlaintext error = apperr.Internal("secretbox.errors.plaintext")

var ErrUnknownKey error = apperr.Internal("secretbox.errors.unknown_key")

type key struct {
	id       string
	material []byte
}

func newKey(material []byte) key {
	sum := sha256.Sum256(material)
	return key{id: hex.EncodeToString(sum[:])[:8], material: material}
}

func parseKeyMaterial(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, apperr.Invalid("secretbox.errors.empty_key")
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) >= 32 {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(raw); err == nil && len(b) >= 32 {
			return b, nil
		}
	}
	if len(raw) >= 32 {
		return []byte(raw), nil
	}
	return nil, apperr.Invalid("secretbox.errors.key_too_short")
}

type keyring struct {
	current  *key
	previous []key
}

func (kr keyring) byID(id string) (key, bool) {
	if kr.current != nil && kr.current.id == id {
		return *kr.current, true
	}
	for _, k := range kr.previous {
		if k.id == id {
			return k, true
		}
	}
	return key{}, false
}

// Read on every call on purpose: caching would make key rotation need a restart.
func loadKeyring() (keyring, error) {
	var kr keyring
	raw := os.Getenv("OCTOPUS_SECRETBOX_KEY")
	if path := strings.TrimSpace(os.Getenv("OCTOPUS_SECRETBOX_KEY_FILE")); path != "" {
		if strings.TrimSpace(raw) != "" {
			return kr, apperr.Invalid("secretbox.errors.key_and_key_file")
		}
		b, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return kr, apperr.Wrap(err, "secretbox.errors.env_var", "name", "OCTOPUS_SECRETBOX_KEY_FILE")
		}
		raw = string(b)
	}
	if strings.TrimSpace(raw) != "" {
		m, err := parseKeyMaterial(raw)
		if err != nil {
			return kr, apperr.Wrap(err, "secretbox.errors.env_var", "name", "OCTOPUS_SECRETBOX_KEY")
		}
		k := newKey(m)
		kr.current = &k
	}
	for i, part := range strings.Split(os.Getenv("OCTOPUS_SECRETBOX_PREVIOUS_KEYS"), ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		m, err := parseKeyMaterial(part)
		if err != nil {
			return kr, apperr.Wrap(err, "secretbox.errors.previous_key_entry", "n", i+1)
		}
		kr.previous = append(kr.previous, newKey(m))
	}
	return kr, nil
}

func ValidateConfig() error {
	kr, err := loadKeyring()
	if err != nil {
		return err
	}
	if kr.current == nil {
		warnNoKey()
	}
	return nil
}

var noKeyOnce sync.Once

func warnNoKey() {
	noKeyOnce.Do(func() {
		log.Printf("[secretbox] %s", i18n.Log("secretbox.log.no_dedicated_key"))
	})
}

func PlaintextAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Must mirror how internal/auth loads OCTOPUS_JWT_SECRET, or v1 values stop decrypting.
func jwtMasterSecret() []byte {
	raw := strings.TrimSpace(os.Getenv("OCTOPUS_JWT_SECRET"))
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) >= 16 {
		return decoded
	}
	return []byte(raw)
}

func gcmFor(material []byte, info string) (cipher.AEAD, error) {
	r := hkdf.New(sha256.New, material, nil, []byte(info))
	k := make([]byte, 32)
	if _, err := io.ReadFull(r, k); err != nil {
		return nil, apperr.Wrap(err, "secretbox.errors.derive_key")
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, apperr.Wrap(err, "secretbox.errors.new_cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, apperr.Wrap(err, "secretbox.errors.new_gcm")
	}
	return gcm, nil
}

func seal(gcm cipher.AEAD, plain string) (string, error) {
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", apperr.Wrap(err, "secretbox.errors.nonce")
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func open(gcm cipher.AEAD, b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", apperr.Wrap(err, "secretbox.errors.decode_secret")
	}
	if len(raw) < gcm.NonceSize() {
		return "", apperr.Internal("secretbox.errors.secret_too_short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", apperr.Wrap(err, "secretbox.errors.decrypt_secret")
	}
	return string(plain), nil
}

func Encrypt(info, plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	kr, err := loadKeyring()
	if err != nil {
		return "", err
	}
	if kr.current == nil {
		warnNoKey()
		gcm, gerr := gcmFor(jwtMasterSecret(), info)
		if gerr != nil {
			return "", gerr
		}
		body, serr := seal(gcm, plain)
		if serr != nil {
			return "", serr
		}
		return v1Prefix + body, nil
	}
	gcm, err := gcmFor(kr.current.material, info)
	if err != nil {
		return "", err
	}
	body, err := seal(gcm, plain)
	if err != nil {
		return "", err
	}
	return v2Prefix + kr.current.id + ":" + body, nil
}

func Decrypt(info, stored string) (string, error) {
	switch {
	case stored == "":
		return "", nil
	case strings.HasPrefix(stored, v2Prefix):
		kid, body, ok := strings.Cut(strings.TrimPrefix(stored, v2Prefix), ":")
		if !ok {
			return "", apperr.Internal("secretbox.errors.malformed_v2")
		}
		kr, err := loadKeyring()
		if err != nil {
			return "", err
		}
		k, found := kr.byID(kid)
		if !found {
			return "", apperr.Wrap(ErrUnknownKey, "secretbox.errors.unknown_key_id", "id", kid)
		}
		gcm, err := gcmFor(k.material, info)
		if err != nil {
			return "", err
		}
		return open(gcm, body)
	case strings.HasPrefix(stored, v1Prefix):
		gcm, err := gcmFor(jwtMasterSecret(), info)
		if err != nil {
			return "", err
		}
		return open(gcm, strings.TrimPrefix(stored, v1Prefix))
	case PlaintextAllowed():
		return stored, nil
	}
	return "", ErrPlaintext
}

func NeedsRewrap(stored string) bool {
	if stored == "" {
		return false
	}
	kr, err := loadKeyring()
	if err != nil {
		return false
	}
	if strings.HasPrefix(stored, v1Prefix) {
		return kr.current != nil
	}
	if rest, ok := strings.CutPrefix(stored, v2Prefix); ok {
		// Without a current key a v2 value stays: rewriting it as v1 would move it back under the
		// JWT secret.
		kid, _, _ := strings.Cut(rest, ":")
		return kr.current != nil && kid != kr.current.id
	}
	return true
}

func CurrentKeyID() string {
	kr, err := loadKeyring()
	if err != nil || kr.current == nil {
		return ""
	}
	return kr.current.id
}

func Mask(secret string) string {
	if len(secret) <= 4 {
		return "…"
	}
	return "…" + secret[len(secret)-4:]
}
