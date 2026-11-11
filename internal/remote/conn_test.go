package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestSignersFingerprint(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(file, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	pub, _ := ssh.NewPublicKey(priv.Public())
	fp := ssh.FingerprintSHA256(pub)

	var conn io.Closer
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	ss, err := Options{IdentityFile: file, KeyFingerprint: fp}.signers(&conn)
	if err != nil || len(ss) != 1 || ssh.FingerprintSHA256(ss[0].PublicKey()) != fp {
		t.Fatalf("matching fingerprint: got %d signers, err %v", len(ss), err)
	}

	ss, err = Options{IdentityFile: file, KeyFingerprint: "SHA256:other"}.signers(&conn)
	if ss != nil || err == nil || !strings.Contains(err.Error(), fp+" (identity_file)") {
		t.Fatalf("foreign fingerprint: got %d signers, err %v", len(ss), err)
	}
}
