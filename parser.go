package main

type ColType int

const (
	COL_INT ColType = iota
	COL_TEXT
)

type Column struct {
	name    string
	colType ColType
}
type Expr struct {
	col string
	op  TokenType
	val string
}
type CreateStmt struct {
	table string
	cols  []Column
}
type InsertStmt struct {
	table string
	cols  []string
	where *Expr
}
type SelectStmt struct {
	table string
	cols  []string
	where *Expr
}
type DeleteStmt struct {
	table string
	where *Expr
}
type UpdateStmt struct {
	table       string
	assignments map[string]string
	where       *Expr
}

type Statement interface {
	stmtNode()
}

func (s *CreateStmt) stmtNode() {}
func (s *InsertStmt) stmtNode() {}
func (s *SelectStmt) stmtNode() {}
func (s *DeleteStmt) stmtNode() {}
func (s *UpdateStmt) stmtNode() {}

type Parser struct {
	tokens []Token
	pos    int
}

func newParser(input string) *Parser {
	l := newLexer(input)
	return &Parser{tokens: l.tokenize(), pos: 0}
}
func (p *Parser) peek() Token {
	if p.pos >= len(p.tokens) {
		return Token{TK_EOF, ""}
	}
	return p.tokens[p.pos]
}
