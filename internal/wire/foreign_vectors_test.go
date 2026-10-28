package wire

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/flynn/noise"
)

type foreignVector struct {
	Protocol string                                 `json:"protocol_name"`
	Hash     string                                 `json:"handshake_hash"`
	Messages []struct{ Payload, Ciphertext string } `json:"messages"`
}
type foreignFixture struct {
	Common  map[string]string `json:"common"`
	Vectors []foreignVector   `json:"vectors"`
}

func vectorHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func loadForeignVectors(t *testing.T) foreignFixture {
	t.Helper()
	b, err := os.ReadFile("testdata/cacophony.json")
	if err != nil {
		t.Fatal(err)
	}
	var f foreignFixture
	if err = json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Vectors) != 2 || f.Vectors[0].Protocol != "Noise_KKpsk2_25519_AESGCM_SHA256" || f.Vectors[1].Protocol != "Noise_KKpsk2_25519_ChaChaPoly_SHA256" {
		t.Fatal("foreign suite inventory changed")
	}
	return f
}

func vectorResponder(t *testing.T, f foreignFixture, v foreignVector, mutation string) (*noise.HandshakeState, *noise.CipherState, *noise.CipherState) {
	t.Helper()
	private, err := ecdh.X25519().NewPrivateKey(vectorHex(t, f.Common["resp_static"]))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(private.PublicKey().Bytes(), vectorHex(t, f.Common["init_remote_static"])) {
		t.Fatal("foreign identity binding")
	}
	psk := vectorHex(t, f.Common["psk"])
	prologue := vectorHex(t, f.Common["prologue"])
	if mutation == "psk" {
		psk[0] ^= 1
	}
	if mutation == "prologue" {
		prologue[0] ^= 1
	}
	cipher := noise.CipherChaChaPoly
	if strings.Contains(v.Protocol, "_AESGCM_") {
		cipher = noise.CipherAESGCM
	}
	return finishVectorResponder(t, f, v, mutation, prologue, cipher, private, psk)
}

func finishVectorResponder(t *testing.T, f foreignFixture, v foreignVector, mutation string, prologue []byte, cipher noise.CipherFunc, private *ecdh.PrivateKey, psk []byte) (*noise.HandshakeState, *noise.CipherState, *noise.CipherState) {
	t.Helper()
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: noise.NewCipherSuite(noise.DH25519, cipher, noise.HashSHA256), Pattern: noise.HandshakeKK, PresharedKeyPlacement: 2, PresharedKey: psk, Prologue: prologue, StaticKeypair: noise.DHKey{Private: private.Bytes(), Public: private.PublicKey().Bytes()}, PeerStatic: vectorHex(t, f.Common["resp_remote_static"]), Random: bytes.NewReader(vectorHex(t, f.Common["resp_ephemeral"]))})
	if err != nil {
		t.Fatal(err)
	}
	payload, _, _, err := hs.ReadMessage(nil, vectorHex(t, v.Messages[0].Ciphertext))
	if mutation == "prologue" {
		if err == nil {
			t.Fatal("wrong prologue accepted")
		}
		return nil, nil, nil
	}
	if err != nil || !bytes.Equal(payload, vectorHex(t, v.Messages[0].Payload)) {
		t.Fatal("foreign message 1", err)
	}
	packet, incoming, outgoing, err := hs.WriteMessage(nil, vectorHex(t, v.Messages[1].Payload))
	if err != nil {
		t.Fatal(err)
	}
	if mutation == "psk" {
		if bytes.Equal(packet, vectorHex(t, v.Messages[1].Ciphertext)) {
			t.Fatal("wrong PSK produced expected keys")
		}
		return hs, incoming, outgoing
	}
	if !bytes.Equal(packet, vectorHex(t, v.Messages[1].Ciphertext)) || !bytes.Equal(hs.ChannelBinding(), vectorHex(t, v.Hash)) {
		t.Fatal("foreign message 2 / transcript hash mismatch")
	}
	return hs, incoming, outgoing
}

func TestIndependentCacophonyKKpsk2Vectors(t *testing.T) {
	f := loadForeignVectors(t)
	for _, v := range f.Vectors {
		t.Run(v.Protocol, func(t *testing.T) {
			if len(v.Messages) != 6 {
				t.Fatal("incomplete foreign transcript")
			}
			_, incoming, outgoing := vectorResponder(t, f, v, "")
			for i := 2; i < 6; i++ {
				if i%2 == 0 {
					plain, err := incoming.Decrypt(nil, nil, vectorHex(t, v.Messages[i].Ciphertext))
					if err != nil || !bytes.Equal(plain, vectorHex(t, v.Messages[i].Payload)) {
						t.Fatal("foreign incoming", i, err)
					}
				} else {
					packet, err := outgoing.Encrypt(nil, nil, vectorHex(t, v.Messages[i].Payload))
					if err != nil || !bytes.Equal(packet, vectorHex(t, v.Messages[i].Ciphertext)) {
						t.Fatal("foreign outgoing", i, err)
					}
				}
			}
			if incoming.Nonce() != 2 || outgoing.Nonce() != 2 {
				t.Fatal("directional nonce progression")
			}
			for _, fault := range []string{"replay", "out-of-order", "tamper", "direction", "psk", "prologue"} {
				t.Run(fault, func(t *testing.T) {
					_, rx, tx := vectorResponder(t, f, v, fault)
					if fault == "prologue" {
						return
					}
					packet := vectorHex(t, v.Messages[2].Ciphertext)
					switch fault {
					case "replay":
						if _, err := rx.Decrypt(nil, nil, packet); err != nil {
							t.Fatal(err)
						}
					case "out-of-order":
						packet = vectorHex(t, v.Messages[4].Ciphertext)
					case "tamper":
						packet[len(packet)-1] ^= 1
					case "direction":
						rx = tx
					}
					if _, err := rx.Decrypt(nil, nil, packet); err == nil {
						t.Fatal("foreign ciphertext fault accepted")
					}
					if fault == "tamper" || fault == "out-of-order" {
						if rx.Nonce() != 0 {
							t.Fatal("failed authentication consumed nonce")
						}
						if _, err := rx.Decrypt(nil, nil, vectorHex(t, v.Messages[2].Ciphertext)); err != nil {
							t.Fatal("primitive failure-state regression", err)
						}
					}
				})
			}
		})
	}
}
