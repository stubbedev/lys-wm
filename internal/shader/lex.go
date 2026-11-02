package shader

import (
	"strings"
)

type tokenKind uint8

const (
	tokEOF tokenKind = iota
	tokIdent
	tokInt
	tokFloat
	tokPunct
)

type token struct {
	kind      tokenKind
	text      string
	line, col int
	from, to  int
}

var puncts = []string{"++", "--", "+=", "-=", "*=", "/=", "<=", ">=", "==", "!=", "&&", "||", "^^", "+", "-", "*", "/", "<", ">", "=", "!", "?", ":", ";", ",", ".", "(", ")", "{", "}", "[", "]"}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

type lexer struct {
	src       []byte
	at        int
	line, col int
	tokens    []token
	blanks    []span
}

func (l *lexer) advance(n int) {
	for range n {
		if l.src[l.at] == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
		l.at++
	}
}

func (l *lexer) fail(format string, args ...any) error {
	return errorAt(l.line, l.col, format, args...)
}

func (l *lexer) rest() string {
	return string(l.src[l.at:])
}

func (l *lexer) directive() error {
	end := strings.IndexByte(l.rest(), '\n')
	if end < 0 {
		end = len(l.src) - l.at
	}
	text := strings.Join(strings.Fields(string(l.src[l.at:l.at+end])), " ")
	if text != "#version 100" {
		return l.fail("%q: the only directive is #version 100; use const instead of #define, and there are no #ifdefs", text)
	}
	l.blanks = append(l.blanks, span{l.at, l.at + end})
	l.advance(end)
	return nil
}

func (l *lexer) number() error {
	start, line, col := l.at, l.line, l.col
	floating := false
	for l.at < len(l.src) && isDigit(l.src[l.at]) {
		l.advance(1)
	}
	if l.at < len(l.src) && l.src[l.at] == '.' {
		floating = true
		l.advance(1)
		for l.at < len(l.src) && isDigit(l.src[l.at]) {
			l.advance(1)
		}
	}
	if l.at < len(l.src) && (l.src[l.at] == 'e' || l.src[l.at] == 'E') {
		floating = true
		l.advance(1)
		if l.at < len(l.src) && (l.src[l.at] == '+' || l.src[l.at] == '-') {
			l.advance(1)
		}
		if l.at >= len(l.src) || !isDigit(l.src[l.at]) {
			return l.fail("an exponent without digits")
		}
		for l.at < len(l.src) && isDigit(l.src[l.at]) {
			l.advance(1)
		}
	}
	if l.at < len(l.src) && isIdentStart(l.src[l.at]) {
		return l.fail("%q after a number: only decimal numbers without suffixes exist", string(l.src[l.at]))
	}
	kind := tokInt
	if floating {
		kind = tokFloat
	}
	l.tokens = append(l.tokens, token{kind: kind, text: string(l.src[start:l.at]), line: line, col: col, from: start, to: l.at})
	return nil
}

func lex(src []byte) ([]token, []span, error) {
	l := &lexer{src: src, line: 1, col: 1}
	for l.at < len(src) {
		c := src[l.at]
		rest := src[l.at:]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			l.advance(1)
		case len(rest) >= 2 && c == '/' && rest[1] == '/':
			for l.at < len(src) && src[l.at] != '\n' {
				l.advance(1)
			}
		case len(rest) >= 2 && c == '/' && rest[1] == '*':
			end := strings.Index(l.rest(), "*/")
			if end < 0 {
				return nil, nil, l.fail("a comment that never ends")
			}
			l.advance(end + 2)
		case c == '#':
			if err := l.directive(); err != nil {
				return nil, nil, err
			}
		case isIdentStart(c):
			start, line, col := l.at, l.line, l.col
			for l.at < len(src) && (isIdentStart(src[l.at]) || isDigit(src[l.at])) {
				l.advance(1)
			}
			l.tokens = append(l.tokens, token{kind: tokIdent, text: string(src[start:l.at]), line: line, col: col, from: start, to: l.at})
		case isDigit(c) || c == '.' && len(rest) >= 2 && isDigit(rest[1]):
			if err := l.number(); err != nil {
				return nil, nil, err
			}
		default:
			found := false
			for _, p := range puncts {
				if strings.HasPrefix(string(rest[:min(2, len(rest))]), p) {
					l.tokens = append(l.tokens, token{kind: tokPunct, text: p, line: l.line, col: l.col, from: l.at, to: l.at + len(p)})
					l.advance(len(p))
					found = true
					break
				}
			}
			if !found {
				return nil, nil, l.fail("unexpected character %q", string(c))
			}
		}
	}
	l.tokens = append(l.tokens, token{kind: tokEOF, line: l.line, col: l.col, from: len(src), to: len(src)})
	return l.tokens, l.blanks, nil
}
