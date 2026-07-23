package subscription

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

// Happ hides a subscription URL behind happ://crypt…/ links so the app that
// opens them can't be told what it is subscribing to. The payload is just the
// same URL under RSA (crypt…crypt4) or RSA+ChaCha20-Poly1305 (crypt5), and the
// keys are public, so we unwrap the link at the boundary and keep everything
// downstream working with a plain URL.
//
//go:embed happ_keys.json
var happKeysJSON []byte

var happKeys struct {
	// PKCS1[0..3] correspond to crypt, crypt2, crypt3, crypt4.
	PKCS1 []string `json:"pkcs1"`
	// Crypt5 maps an 8-byte marker carried by the link to its PKCS#8 key.
	Crypt5 map[string]string `json:"crypt5"`
}

var happKeysOnce sync.Once

func loadHappKeys() {
	happKeysOnce.Do(func() {
		if err := json.Unmarshal(happKeysJSON, &happKeys); err != nil {
			panic("happ keys: " + err.Error()) // embedded data, broken only at build time
		}
	})
}

// unwrapHapp returns the URL an encrypted happ link carries, or raw unchanged.
// It sits in front of the fetch paths so nothing downstream has to know the
// format exists; the menu unwraps at add time instead, to store a readable URL.
func unwrapHapp(raw string) (string, error) {
	if !IsHappLink(raw) {
		return raw, nil
	}
	return DecryptHappLink(raw)
}

// IsHappLink reports whether raw is an encrypted Happ deep link.
func IsHappLink(raw string) bool {
	return strings.HasPrefix(raw, "happ://crypt")
}

// DecryptHappLink turns a happ://crypt…/ link into the URL (or bare link) it wraps.
func DecryptHappLink(raw string) (string, error) {
	loadHappKeys()
	path := strings.TrimPrefix(raw, "happ://")
	for i, prefix := range []string{"crypt/", "crypt2/", "crypt3/", "crypt4/"} {
		if strings.HasPrefix(path, prefix) {
			return happDecryptRSA(i, path[len(prefix):])
		}
	}
	if strings.HasPrefix(path, "crypt5/") {
		return happDecryptCrypt5(path[len("crypt5/"):])
	}
	return "", fmt.Errorf("неизвестный формат happ-ссылки: %s", raw)
}

// happB64 decodes both the standard and the URL-safe alphabet, with or without
// padding — the Happ payloads mix all four.
func happB64(s string) ([]byte, error) {
	s = strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "", "-", "+", "_", "/").Replace(s)
	s = strings.TrimRight(s, "=")
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return base64.StdEncoding.DecodeString(s)
}

func happPrivateKey(pemBody string, pkcs8 bool) (*rsa.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(pemBody)
	if err != nil {
		return nil, err
	}
	if !pkcs8 {
		return x509.ParsePKCS1PrivateKey(der)
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("ключ не RSA")
	}
	return rsaKey, nil
}

// happDecryptRSA handles crypt…crypt4: base64 payload split into RSA blocks.
func happDecryptRSA(ordinal int, payload string) (string, error) {
	key, err := happPrivateKey(happKeys.PKCS1[ordinal], false)
	if err != nil {
		return "", fmt.Errorf("ключ happ: %w", err)
	}
	cipher, err := happB64(payload)
	if err != nil {
		return "", fmt.Errorf("happ base64: %w", err)
	}
	blockSize := key.Size()
	if len(cipher) == 0 || len(cipher)%blockSize != 0 {
		return "", fmt.Errorf("длина happ-payload не кратна размеру ключа")
	}
	var out []byte
	for i := 0; i < len(cipher); i += blockSize {
		plain, err := rsa.DecryptPKCS1v15(rand.Reader, key, cipher[i:i+blockSize])
		if err != nil {
			return "", fmt.Errorf("расшифровка happ-ссылки: %w", err)
		}
		out = append(out, plain...)
	}
	return string(out), nil
}

// happSwapAdjacent swaps every byte pair: ABCD → BADC. Self-inverse.
func happSwapAdjacent(b []byte) []byte {
	out := append([]byte(nil), b...)
	for i := 0; i+1 < len(out); i += 2 {
		out[i], out[i+1] = out[i+1], out[i]
	}
	return out
}

// happSwapHalves swaps the halves of every complete 4-byte block: ABCD → CDAB.
// Self-inverse.
func happSwapHalves(b []byte) []byte {
	out := append([]byte(nil), b...)
	full := len(out) - len(out)%4
	for i := 0; i < full; i += 4 {
		out[i], out[i+2] = out[i+2], out[i]
		out[i+1], out[i+3] = out[i+3], out[i+1]
	}
	return out
}

func happDecryptCrypt5(payload string) (string, error) {
	shuffled := happSwapHalves([]byte(payload))
	if len(shuffled) < 8 {
		return "", fmt.Errorf("happ crypt5: payload слишком короткий")
	}
	marker := string(shuffled[:4]) + string(shuffled[len(shuffled)-4:])
	keyB64, ok := happKeys.Crypt5[marker]
	if !ok {
		return "", fmt.Errorf("happ crypt5: неизвестный маркер %q", marker)
	}
	key, err := happPrivateKey(keyB64, true)
	if err != nil {
		return "", fmt.Errorf("ключ happ crypt5: %w", err)
	}

	// The salted layout inserts 10 bytes after the nonce. Which one a link uses
	// is not flagged, so we guess from the byte where the unsalted layout would
	// start its decimal length and fall back to the other reading.
	body := shuffled[4 : len(shuffled)-4]
	preferSalted := len(body) > 12 && !(body[12] >= '0' && body[12] <= '9')
	first, err := happCrypt5Body(body, key, preferSalted)
	if err == nil {
		return first, nil
	}
	if second, err2 := happCrypt5Body(body, key, !preferSalted); err2 == nil {
		return second, nil
	}
	return "", err
}

func happCrypt5Body(body []byte, key *rsa.PrivateKey, salted bool) (string, error) {
	if len(body) < 13 {
		return "", fmt.Errorf("happ crypt5: тело слишком короткое")
	}
	nonce := body[:12]
	var salt []byte
	lengthStart := 12
	if salted {
		if len(body) < 22 {
			return "", fmt.Errorf("happ crypt5: солёный заголовок слишком короткий")
		}
		salt = body[14:22]
		lengthStart = 22
	}

	lengthEnd := lengthStart
	for lengthEnd < len(body) && body[lengthEnd] >= '0' && body[lengthEnd] <= '9' {
		lengthEnd++
	}
	if lengthEnd == lengthStart {
		return "", fmt.Errorf("happ crypt5: отсутствует длина сегмента")
	}
	segLen, err := strconv.Atoi(string(body[lengthStart:lengthEnd]))
	if err != nil {
		return "", fmt.Errorf("happ crypt5: длина сегмента: %w", err)
	}
	packed := body[lengthEnd:]
	if len(packed) == 0 || segLen < 0 || segLen > len(packed)-1 {
		return "", fmt.Errorf("happ crypt5: сегмент обрезан")
	}

	rsaCipher, err := happB64(string(packed[segLen+1:]))
	if err != nil {
		return "", fmt.Errorf("happ crypt5 base64 ключа: %w", err)
	}
	rsaPlain, err := rsa.DecryptPKCS1v15(rand.Reader, key, rsaCipher)
	if err != nil {
		return "", fmt.Errorf("happ crypt5: расшифровка ключа: %w", err)
	}
	chachaKey, err := happB64(string(happSwapAdjacent(rsaPlain)))
	if err != nil {
		return "", fmt.Errorf("happ crypt5 base64 ключа: %w", err)
	}
	if len(chachaKey) != chacha20poly1305.KeySize {
		return "", fmt.Errorf("happ crypt5: длина ключа %d", len(chachaKey))
	}
	if salt != nil {
		for i := range chachaKey {
			chachaKey[i] ^= salt[i%len(salt)]
		}
	}

	ciphertext, err := happB64(string(packed[1 : segLen+1]))
	if err != nil {
		return "", fmt.Errorf("happ crypt5 base64 данных: %w", err)
	}
	aead, err := chacha20poly1305.New(chachaKey)
	if err != nil {
		return "", err
	}
	intermediate, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("happ crypt5: проверка подлинности не прошла")
	}
	plain, err := happB64(string(happSwapAdjacent(intermediate)))
	if err != nil {
		return "", fmt.Errorf("happ crypt5 base64 результата: %w", err)
	}
	return string(plain), nil
}
