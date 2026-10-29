// Package cpace implements Sendspin's exact draft-21 CPACE-X25519-SHA512
// initiator/responder transcript, with explicit mutual confirmation. It is not
// a general PAKE API. Low-level field arithmetic and X25519 are delegated to
// edwards25519/field and crypto/ecdh; no variable-time big integers are used.
package cpace

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"errors"

	"filippo.io/edwards25519/field"
)

var ErrInvalid = errors.New("cpace: invalid share, transcript or confirmation")

func lv(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = binary.AppendUvarint(out, uint64(len(part)))
		out = append(out, part...)
	}
	return out
}
func generator(prs, ci, sid []byte) []byte {
	dsi := []byte("CPace255")
	padding := max(0, 128-len(lv(dsi))-len(lv(prs))-1)
	h := sha512.Sum512(lv(dsi, prs, make([]byte, padding), ci, sid))
	h[31] &= 127
	r, _ := new(field.Element).SetBytes(h[:32])
	one := new(field.Element).One()
	var aBytes [32]byte
	binary.LittleEndian.PutUint64(aBytes[:], 486662)
	a, _ := new(field.Element).SetBytes(aBytes[:])
	r2 := new(field.Element).Square(r)
	den := new(field.Element).Add(r2, r2)
	den.Add(den, one)
	v := new(field.Element).Invert(den)
	v.Multiply(v, a)
	v.Negate(v)
	v2 := new(field.Element).Square(v)
	rhs := new(field.Element).Add(v, a)
	rhs.Multiply(rhs, v2)
	rhs.Add(rhs, v)
	_, square := new(field.Element).SqrtRatio(rhs, one)
	other := new(field.Element).Add(v, a)
	other.Negate(other)
	x := new(field.Element).Select(v, other, square)
	// The draft's exceptional Legendre-symbol-zero result is -A/2.
	two := new(field.Element).Add(one, one)
	half := new(field.Element).Invert(two)
	exceptional := new(field.Element).Multiply(a, half)
	exceptional.Negate(exceptional)
	x.Select(exceptional, x, rhs.Equal(new(field.Element)))
	clear(h[:])
	return x.Bytes()
}

// Result remains private until Verify succeeds. The client must never wrap or
// disclose credentials from an unconfirmed PAKE, even over encrypted Sentinel.
type Result struct {
	isk, mac              [64]byte
	sid, ya, yb, ada, adb []byte
	verified, finished    bool
}

func Respond(prs, sid, ya []byte) ([]byte, *Result, error) {
	var scalar [32]byte
	if _, err := rand.Read(scalar[:]); err != nil {
		return nil, nil, err
	}
	defer clear(scalar[:])
	return respondScalar(prs, sid, ya, scalar[:], nil, []byte("server"), []byte("client"))
}
func respondScalar(prs, sid, ya, scalar, ci, ada, adb []byte) ([]byte, *Result, error) {
	if len(ya) != 32 || len(scalar) != 32 {
		return nil, nil, ErrInvalid
	}
	key, err := ecdh.X25519().NewPrivateKey(scalar)
	if err != nil {
		return nil, nil, ErrInvalid
	}
	g, err := ecdh.X25519().NewPublicKey(generator(prs, ci, sid))
	if err != nil {
		return nil, nil, ErrInvalid
	}
	yb, err := key.ECDH(g)
	if err != nil {
		return nil, nil, ErrInvalid
	}
	peer, err := ecdh.X25519().NewPublicKey(ya)
	if err != nil {
		return nil, nil, ErrInvalid
	}
	shared, err := key.ECDH(peer)
	if err != nil {
		return nil, nil, ErrInvalid
	}
	defer clear(shared)
	transcript := append(lv(ya, ada), lv(yb, adb)...)
	input := append(lv([]byte("CPace255_ISK"), sid, shared), transcript...)
	r := &Result{sid: append([]byte(nil), sid...), ya: append([]byte(nil), ya...), yb: append([]byte(nil), yb...), ada: append([]byte(nil), ada...), adb: append([]byte(nil), adb...), isk: sha512.Sum512(input)}
	clear(input)
	macInput := append(append([]byte("CPaceMac"), sid...), r.isk[:]...)
	r.mac = sha512.Sum512(macInput)
	clear(macInput)
	return yb, r, nil
}
func (r *Result) tag(a bool) []byte {
	h := hmac.New(sha512.New, r.mac[:])
	if a {
		_, _ = h.Write(lv(r.ya, r.ada))
	} else {
		_, _ = h.Write(lv(r.yb, r.adb))
	}
	return h.Sum(nil)
}
func (r *Result) Verify(tag []byte) bool {
	if r == nil || r.finished {
		return false
	}
	r.finished = true
	// Explicit MCF must not accept our own tag as the peer's proof when both
	// public shares/AD tuples coincide. Sendspin's distinct fixed role labels
	// already prevent that tuple; retain the draft's guard in the primitive too.
	reflection := hmac.Equal(r.ya, r.yb) && hmac.Equal(r.ada, r.adb)
	r.verified = !reflection && len(tag) == 64 && hmac.Equal(tag, r.tag(true))
	if !r.verified {
		r.Close()
	}
	return r.verified
}
func (r *Result) Confirmed() (isk [64]byte, clientTag []byte, err error) {
	if r == nil || !r.verified {
		return isk, nil, ErrInvalid
	}
	return r.isk, r.tag(false), nil
}
func (r *Result) Close() {
	if r != nil {
		clear(r.isk[:])
		clear(r.mac[:])
		r.verified = false
		r.finished = true
	}
}
