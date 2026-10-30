package dynlib

import "unsafe"

// CString reads bounded native diagnostic/name storage, never media buffers.
func CString(p *byte) string {
	if p == nil {
		return ""
	}
	b := make([]byte, 0, 64)
	for i := uintptr(0); i < 4096; i++ {
		v := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if v == 0 {
			return string(b)
		}
		b = append(b, v)
	}
	return "[unterminated native string]"
}
