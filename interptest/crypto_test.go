package interptest

import "testing"

// crypto/* is bridged natively on wasm (un-dropped from the crypto WasmDrop
// prefix): its functions are leaf computations ([]byte in, digest/ciphertext
// out) that never dispatch an interpreted method, so no shared-PC trap. hash/*
// is likewise bridged. (compress/* and archive/* CANNOT be bridged on wasm --
// they consume io.Reader/Writer, which are interpreted there, and native code
// dispatching an interpreted Read traps; they need mirroring instead.)
// This TestSynth* case runs under the wasm CI: native and wasm both bridge crypto.

func TestSynthCrypto(t *testing.T) {
	const src = `package main
import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"hash/fnv"
)
func main() {
	sum := sha256.Sum256([]byte("hello"))
	fmt.Println("sha256", hex.EncodeToString(sum[:]))

	mac := hmac.New(sha256.New, []byte("key"))
	mac.Write([]byte("msg"))
	fmt.Println("hmac", hex.EncodeToString(mac.Sum(nil)))

	block, _ := aes.NewCipher([]byte("0123456789abcdef"))
	ct := make([]byte, 16)
	cipher.NewCTR(block, make([]byte, aes.BlockSize)).XORKeyStream(ct, []byte("plaintext1234567"))
	fmt.Println("aes", hex.EncodeToString(ct))

	priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	sig := ed25519.Sign(priv, []byte("message"))
	ok := ed25519.Verify(priv.Public().(ed25519.PublicKey), []byte("message"), sig)
	fmt.Println("ed25519", hex.EncodeToString(sig[:8]), ok)

	fmt.Println("crc32", crc32.ChecksumIEEE([]byte("hello")))
	h := fnv.New32a()
	h.Write([]byte("hello"))
	fmt.Println("fnv32a", h.Sum32())
}`
	want := "sha256 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\n" +
		"hmac 2d93cbc1be167bcb1637a4a23cbff01a7878f0c50ee833954ea5221bb1b8c628\n" +
		"aes 7bf774b32530c58d612cfdf7f42a03e2\n" +
		"ed25519 24fbab0609c71311 true\n" +
		"crc32 907060870\n" +
		"fnv32a 1335831723\n"
	if got := evalOut(t, "crypto.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
