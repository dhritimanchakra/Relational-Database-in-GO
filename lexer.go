package main

import (
	"fmt"
	"strings"
)

type TokenType int

const (
	TK_EOF TokenType = iota
	TK_IDENT
	TK_NUMBER
	TK_STRING

	// keywords
	TK_CREATE
	TK_TABLE
	TK_INSERT
	TK_INTO
	TK_VALUES
	TK_SELECT
	TK_FROM
	TK_WHERE
	TK_AND
	TK_DELETE
	TK_UPDATE
	TK_SET
	TK_BEGIN
	TK_COMMIT
	TK_ROLLBACK

	// symbols
	TK_LPAREN
	TK_RPAREN
	TK_COMMA
	TK_STAR
	TK_SEMI
	TK_EQ // =
	TK_NE // != or <>
	TK_LT // <
	TK_LE // <=
	TK_GT // >
	TK_GE // >=
)

type Token struct {
	typ TokenType
	val string
}

var keywords = map[string]TokenType{
	"create": TK_CREATE, "table": TK_TABLE, "insert": TK_INSERT, "into": TK_INTO,
	"values": TK_VALUES, "select": TK_SELECT, "from": TK_FROM, "where": TK_WHERE,
	"and": TK_AND, "delete": TK_DELETE, "update": TK_UPDATE, "set": TK_SET,
	"begin": TK_BEGIN, "commit": TK_COMMIT, "rollback": TK_ROLLBACK,
}

const maxKeywordLen = 8 // "rollback"

type Lexer struct {
	in  string
	pos int
}

func newLexer(input string) *Lexer { return &Lexer{in: input} }

func (l *Lexer) tokenize() ([]Token, error) {
	toks := make([]Token, 0, len(l.in)/3+2)
	for {
		tok, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, tok)
		if tok.typ == TK_EOF {
			return toks, nil
		}
	}
}

func isSpace(c byte) bool      { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) }

func (l *Lexer) next() (Token, error) {
	in := l.in
	for l.pos < len(in) && isSpace(in[l.pos]) {
		l.pos++
	}
	if l.pos >= len(in) {
		return Token{TK_EOF, ""}, nil
	}
	start := l.pos
	c := in[start]

	switch {
	case isIdentStart(c):
		for l.pos < len(in) && isIdentPart(in[l.pos]) {
			l.pos++
		}
		word := in[start:l.pos]
		if t, ok := lookupKeyword(word); ok {
			return Token{t, word}, nil
		}
		return Token{TK_IDENT, lowerASCII(word)}, nil // identifiers are case-insensitive

	case isDigit(c) || (c == '-' && start+1 < len(in) && isDigit(in[start+1])):
		l.pos++
		for l.pos < len(in) && isDigit(in[l.pos]) {
			l.pos++
		}
		return Token{TK_NUMBER, in[start:l.pos]}, nil

	case c == '\'':
		return l.lexString()
	}

	l.pos++
	peek := byte(0)
	if l.pos < len(in) {
		peek = in[l.pos]
	}
	switch c {
	case '(':
		return Token{TK_LPAREN, "("}, nil
	case ')':
		return Token{TK_RPAREN, ")"}, nil
	case ',':
		return Token{TK_COMMA, ","}, nil
	case '*':
		return Token{TK_STAR, "*"}, nil
	case ';':
		return Token{TK_SEMI, ";"}, nil
	case '=':
		return Token{TK_EQ, "="}, nil
	case '<':
		if peek == '=' {
			l.pos++
			return Token{TK_LE, "<="}, nil
		}
		if peek == '>' {
			l.pos++
			return Token{TK_NE, "<>"}, nil
		}
		return Token{TK_LT, "<"}, nil
	case '>':
		if peek == '=' {
			l.pos++
			return Token{TK_GE, ">="}, nil
		}
		return Token{TK_GT, ">"}, nil
	case '!':
		if peek == '=' {
			l.pos++
			return Token{TK_NE, "!="}, nil
		}
	}
	return Token{}, fmt.Errorf("unexpected character %q at position %d", c, start)
}

func (l *Lexer) lexString() (Token, error) {
	in := l.in
	start := l.pos
	l.pos++
	seg := l.pos
	var buf []byte
	escaped := false
	for l.pos < len(in) {
		if in[l.pos] == '\'' {
			if l.pos+1 < len(in) && in[l.pos+1] == '\'' {
				buf = append(buf, in[seg:l.pos+1]...)
				escaped = true
				l.pos += 2
				seg = l.pos
				continue
			}
			var s string
			if escaped {
				buf = append(buf, in[seg:l.pos]...)
				s = string(buf)
			} else {
				s = in[seg:l.pos]
			}
			l.pos++ // closing quote
			return Token{TK_STRING, s}, nil
		}
		l.pos++
	}
	return Token{}, fmt.Errorf("unterminated string starting at position %d", start)
}

func lookupKeyword(word string) (TokenType, bool) {
	if len(word) > maxKeywordLen {
		return 0, false
	}
	var buf [maxKeywordLen]byte
	for i := 0; i < len(word); i++ {
		b := word[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		buf[i] = b
	}
	t, ok := keywords[string(buf[:len(word)])]
	return t, ok
}

func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}
