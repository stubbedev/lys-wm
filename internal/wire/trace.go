package wire

import (
	"bytes"
	"encoding/binary"
	"strconv"
)

func AppendArgs(dst []byte, args []Arg, body []byte) []byte {
	word := func() (uint32, bool) {
		if len(body) < 4 {
			return 0, false
		}
		v := binary.NativeEndian.Uint32(body)
		body = body[4:]
		return v, true
	}
	blob := func() ([]byte, bool, bool) {
		n, ok := word()
		if !ok || uint64(len(body)) < (uint64(n)+3)&^3 {
			return nil, false, false
		}
		b := body[:n]
		body = body[pad(int(n)):]
		return b, n == 0, true
	}
	object := func(iface string, id uint32) {
		if id == 0 {
			dst = append(dst, "nil"...)
			return
		}
		dst = append(dst, iface...)
		dst = append(dst, '@')
		dst = strconv.AppendUint(dst, uint64(id), 10)
	}
	for i, a := range args {
		if i > 0 {
			dst = append(dst, ", "...)
		}
		ok := true
		switch a.Type {
		case ArgInt:
			var v uint32
			if v, ok = word(); ok {
				dst = strconv.AppendInt(dst, int64(int32(v)), 10)
			}
		case ArgUint:
			var v uint32
			if v, ok = word(); ok {
				dst = strconv.AppendUint(dst, uint64(v), 10)
			}
		case ArgFixed:
			var v uint32
			if v, ok = word(); ok {
				dst = strconv.AppendFloat(dst, Fixed(v).Float(), 'f', -1, 64)
			}
		case ArgString:
			var b []byte
			var null bool
			b, null, ok = blob()
			if ok && null {
				dst = append(dst, "nil"...)
			} else if ok {
				dst = strconv.AppendQuote(dst, string(bytes.TrimSuffix(b, []byte{0})))
			}
		case ArgObject:
			var v uint32
			if v, ok = word(); ok {
				object(a.Interface, v)
			}
		case ArgNewID:
			iface := a.Interface
			if iface == "" {
				var b []byte
				b, _, ok = blob()
				iface = string(bytes.TrimSuffix(b, []byte{0}))
				if ok {
					_, ok = word()
				}
			}
			var v uint32
			if ok {
				v, ok = word()
			}
			if ok {
				dst = append(dst, "new id "...)
				object(iface, v)
			}
		case ArgArray:
			var b []byte
			if b, _, ok = blob(); ok {
				dst = append(dst, "array["...)
				dst = strconv.AppendInt(dst, int64(len(b)), 10)
				dst = append(dst, ']')
			}
		case ArgFD:
			dst = append(dst, "fd"...)
		}
		if !ok {
			return append(dst, "<truncated>"...)
		}
	}
	return dst
}
