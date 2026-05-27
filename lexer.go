package main

import "fmt"

type TokenType int

const (
	TK_SELECT TokenType = iota
	TK_INSERT
	TK_DELETE
	TK_UPDATE
	TK_CREATE
	TK_TABLE
	TK_INTO
	TK_VALUES
	TK_FROM
	TK_WHERE
	TK_SET
	TK_AND
	TK_INT
	TK_TEXT
	TK_STAR
	TK_COMMA
	TK_LPAREN
	TK_RPAREN
	TK_EQ
	TK_GT
	TK_LT
	TK_IDENT
	TK_NUMBER
	TK_STRING
	TK_EOF
)

type Token struct {
	typ TokenType
	val string
}

type Lexer struct {
	input []rune
	pos   int
}

func newLexer(input string) *Lexer {
	return &Lexer{input: []rune(input), pos: 0}
}

func (l *Lexer) skipWhitespace() {
	for l.pos < len(l.input) && (l.input[l.pos] == ' ' || l.input[l.pos] == '\t' || l.input[l.pos] == '\n') {
		l.pos++
	}
}

func (l *Lexer) nextToken() Token {
	l.skipWhitespace()
	if l.pos >= len(l.input) {
		return Token{TK_EOF, ""}
	}
	ch := l.input[l.pos]
	switch ch {
	case '*':
		l.pos++
		return Token{TK_STAR, "*"}
	case ',':
		l.pos++
		return Token{TK_COMMA, ","}
	case '(':
		l.pos++
		return Token{TK_LPAREN, "("}
	case ')':
		l.pos++
		return Token{TK_RPAREN, ")"}
	case '=':
		l.pos++
		return Token{TK_EQ, "="}
	case '>':
		l.pos++
		return Token{TK_GT, ">"}
	case '<':
		l.pos++
		return Token{TK_LT, "<"}
	case '"':
		l.pos++
		start := l.pos
		for l.pos < len(l.input) && l.input[l.pos] != '"' {
			l.pos++
		}
		s := string(l.input[start:l.pos])
		l.pos++
		return Token{TK_STRING, s}
	}
	if ch >= '0' && ch <= '9' {
		start := l.pos
		for l.pos < len(l.input) && l.input[l.pos] >= '0' && l.input[l.pos] <= '9' {
			l.pos++
		}
		return Token{TK_NUMBER, string(l.input[start:l.pos])}
	}
	if isLetter(ch) {
		start := l.pos
		for l.pos < len(l.input) && (isLetter(l.input[l.pos]) || (l.input[l.pos] >= '0' && l.input[l.pos] <= '9') || l.input[l.pos] == '_') {
			l.pos++
		}
		word := string(l.input[start:l.pos])
		return Token{keywordOrIdent(word), word}
	}
	panic(fmt.Sprintf("unexpected character: %c", ch))
}


func (l*Lexer) tokenize()[]Token{
	var tokens[] Token
	for{
		tok:=l.nextToken()
		tokens=append(tokens,tok)
		if 
	}
}