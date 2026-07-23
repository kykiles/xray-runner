package subscription

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"testing"
)

// A real salted crypt5 link wrapping https://example.com/sub.
const crypt5Link = "happ://crypt5/fzvdO4bMOfTWNaB3taWRhRaF64soexaE9dm3ZlLK0Rke9Rz3BG1f9gmj4tSDpjRSWWdX5G6oaaQK9Gs+fdJPWNXIF08BsaeifWtfTlCvC/nSWDv0ZrofgJXkQ8MlUk63CoJkt7RvXAfablYXb/cYWEZJQkDMyMaE5/1KegAbWVWVI60MPqDylUyYoLtOeOOX9amvELecOZ4kKz1QVqgE9uCBz3py+3Ghr1iVGKOhFwb98OFP+j0tGvDo/3d609DVq3RwBGXu1ogZ7PTc3/A5IlaA3Hff5IlVujozQ3ywmQBsTGd+l3AHJAX1oDbPkRSSwg7Y7hl3AKXKpZsEhMzPbJY8UxZ7GmVsxeROLopVx85ACqakzg+ZZwdZslfKgdRzUmL9Mv895HDOHE3tbh6qnDhE9Ew/Epx1iBCb2HjorOLDBluH8ztdL9mdUX+turjC4GLN0YR55P3H23A0W5zl0di5YfrI2nUBKxh30lUVG9NbqYKlwxgmhxAUZrQ6cnFGF2VuZ9VJLQ3sQ8rqXTtfau8ySbu770Hd6vVEun8aSJm4W4M0DagKbORL4A4M6Cuf1v/jj7EWhA9yhhcSuxkc6WUWMGYRraWBM9vSrSbjeT1U69a3n9T56M/TOJWf4z8fXrHRnCR1tfzwrjHiOJfZGiKvbAd4k6f+VADYpLdCq+ornEElv7V0sByfwPTgep+Q33Qkl67ArHpmbZDcCyWkGz0BoUzGJFe58YiS/oNFVdufbuDnFd1ArAVMztJJJbxlo4Is48+Iof=ff"

func TestDecryptHappCrypt5(t *testing.T) {
	got, err := DecryptHappLink(crypt5Link)
	if err != nil {
		t.Fatalf("DecryptHappLink: %v", err)
	}
	if want := "https://example.com/sub"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDecryptHappCrypt5Tampered(t *testing.T) {
	tampered := crypt5Link[:200] + "A" + crypt5Link[201:]
	if _, err := DecryptHappLink(tampered); err == nil {
		t.Fatal("испорченная ссылка расшифровалась без ошибки")
	}
}

// crypt…crypt4 are plain RSA, so a round-trip through the matching public key
// proves both the key table and the block loop.
func TestDecryptHappCrypt1to4(t *testing.T) {
	loadHappKeys()
	url := "https://example.com/sub?token=abc"
	for i, prefix := range []string{"crypt", "crypt2", "crypt3", "crypt4"} {
		key, err := happPrivateKey(happKeys.PKCS1[i], false)
		if err != nil {
			t.Fatalf("%s: ключ: %v", prefix, err)
		}
		cipher, err := rsa.EncryptPKCS1v15(rand.Reader, &key.PublicKey, []byte(url))
		if err != nil {
			t.Fatalf("%s: шифрование: %v", prefix, err)
		}
		link := "happ://" + prefix + "/" + base64.StdEncoding.EncodeToString(cipher)
		got, err := DecryptHappLink(link)
		if err != nil {
			t.Fatalf("%s: %v", prefix, err)
		}
		if got != url {
			t.Fatalf("%s: got %q, want %q", prefix, got, url)
		}
	}
}

func TestIsHappLink(t *testing.T) {
	if !IsHappLink(crypt5Link) {
		t.Fatal("crypt5-ссылка не распознана")
	}
	if IsHappLink("https://example.com/sub") {
		t.Fatal("обычный URL принят за happ-ссылку")
	}
}
