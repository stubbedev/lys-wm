package ipc

import (
	"strconv"
	"unicode/utf8"
)

type lineAppender interface {
	appendFields(dst []byte) []byte
}

func (e ModeEvent) appendFields(dst []byte) []byte {
	dst = appendUintField(dst, "window", uint64(e.Window))
	dst = appendBoolField(dst, "floating", e.Floating)
	dst = appendBoolField(dst, "fullscreen", e.Fullscreen)
	dst = appendBoolField(dst, "maximized", e.Maximized)
	return appendBoolField(dst, "client_fullscreen", e.ClientFullscreen)
}

func (e LayerEvent) appendFields(dst []byte) []byte {
	dst = appendStringField(dst, "change", string(e.Change))
	dst = appendUintField(dst, "id", e.ID)
	dst = appendStringField(dst, "namespace", e.Namespace)
	dst = appendStringField(dst, "output", e.Output)
	return appendStringField(dst, "layer", string(e.Layer))
}

func appendKey(dst []byte, key string) []byte {
	dst = append(dst, ',', '"')
	dst = append(dst, key...)
	return append(dst, '"', ':')
}

func appendUintField(dst []byte, key string, v uint64) []byte {
	return strconv.AppendUint(appendKey(dst, key), v, 10)
}

func appendBoolField(dst []byte, key string, v bool) []byte {
	return strconv.AppendBool(appendKey(dst, key), v)
}

func appendStringField(dst []byte, key, v string) []byte {
	return appendJSONString(appendKey(dst, key), v)
}

const hexDigits = "0123456789abcdef"

func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if htmlSafe(b) {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			dst = append(dst, s[start:i]...)
			dst = append(dst, `�`...)
		case r == ' ' || r == ' ':
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xf])
		default:
			i += size
			continue
		}
		i += size
		start = i
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

func htmlSafe(b byte) bool {
	return b >= 0x20 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&'
}
