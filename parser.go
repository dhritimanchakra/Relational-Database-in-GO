package main


import (
	"fmt"
	"strconv"
)

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
	vals []string
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

func (p *Parser) consume() Token{
	tok:=p.peek()
	p.pos++
	return tok
}
func (p *Parser) expect(typ TokenType) Token{
	tok:=p.consume()
	if tok.typ!=typ{
			panic(fmt.Sprintf("expected token %d got %d (%q)", typ, tok.typ, tok.val))
	}
	return tok

}

func (p *Parser) parse() Statement{
	tok:=p.peek()
	switch tok.typ {
	case TK_CREATE:
		return p.parseCreate()
	case TK_INSERT:
		return p.parseInsert()
	case TK_SELECT:
		return p.parseSelect()
	case TK_DELETE:
		return p.parseDelete()
	case TK_UPDATE:
		return p.parseUpdate()
	}
	panic(fmt.Sprintf("unexpected token: %q", tok.val))

}

func (p *Parser) parseCreate() *CreateStmt{
	p.expect(TK_CREATE)
	p.expect(TK_TABLE)
	name:=p.expect(TK_IDENT)
	p.expect(TK_LPAREN)
	var cols []Column
	for p.peek().typ!=TK_RPAREN{
		colName:=p.expect(TK_IDENT)
		colTok:=p.consume()
		var ct ColType
				switch colTok.typ {
		case TK_INT:
			ct = COL_INT
		case TK_TEXT:
			ct = COL_TEXT
		default:
			panic(fmt.Sprintf("unknown column type: %q", colTok.val))
		}
		cols = append(cols, Column{name: colName.val, colType: ct})
		if p.peek().typ == TK_COMMA {
			p.consume()
		}

	}	
	p.expect(TK_RPAREN)
	return &CreateStmt{table: name.val, cols: cols}

}
func (p *Parser) parseInsert() *InsertStmt {
	p.expect(TK_INSERT)
	p.expect(TK_INTO)
	name := p.expect(TK_IDENT)
	p.expect(TK_VALUES)
	p.expect(TK_LPAREN)
	var vals []string
	for p.peek().typ != TK_RPAREN {
		tok := p.consume()
		if tok.typ != TK_NUMBER && tok.typ != TK_STRING {
			panic(fmt.Sprintf("expected value got %q", tok.val))
		}
		vals = append(vals, tok.val)
		if p.peek().typ == TK_COMMA {
			p.consume()
		}
	}
	p.expect(TK_RPAREN)
	return &InsertStmt{table: name.val, vals: vals}
}


func (p *Parser) parseSelect() *SelectStmt{
	p.expect(TK_SELECT)
	var cols []string
	if p.peek(),typ==TK_STAR{
		p.consume()
		cols=[]string{"*"}
	}else{
		for{
			col:=p.expect(TK_IDENT)
			cols=append(cols,cols.val)
			if p.peek().typ!=TK_COMMA{
				break
			}
			p.consume()
		}
	}
	p.expect(TK_FROM)
	name:=p.expect(TK_IDENT)
	var where *Expr
	if p.peek().typ==TK_WHERE{
		p.consume()
		where=p.parseExpr()
	}
	return &SelectStmt{table:name,cols:cols,where:where}
}

func (p *Parser) parseDelete() *DeleteStmt {
	p.expect(TK_DELETE)
	p.expect(TK_FROM)
	name := p.expect(TK_IDENT)
	var where *Expr
	if p.peek().typ == TK_WHERE {
		p.consume()
		where = p.parseExpr()
	}
	return &DeleteStmt{table: name.val, where: where}
}
 
func (p *Parser) parseUpdate() *UpdateStmt {
	p.expect(TK_UPDATE)
	name := p.expect(TK_IDENT)
	p.expect(TK_SET)
	assignments := map[string]string{}
	for {
		col := p.expect(TK_IDENT)
		p.expect(TK_EQ)
		val := p.consume()
		if val.typ != TK_NUMBER && val.typ != TK_STRING {
			panic(fmt.Sprintf("expected value got %q", val.val))
		}
		assignments[col.val] = val.val
		if p.peek().typ != TK_COMMA {
			break
		}
		p.consume()
	}
	var where *Expr
	if p.peek().typ == TK_WHERE {
		p.consume()
		where = p.parseExpr()
	}
	return &UpdateStmt{table: name.val, assignments: assignments, where: where}
}
func (p *Parser) parseExpr() *Expr {
	col := p.expect(TK_IDENT)
	op := p.consume()
	if op.typ != TK_EQ && op.typ != TK_GT && op.typ != TK_LT {
		panic(fmt.Sprintf("expected operator got %q", op.val))
	}
	val := p.consume()
	if val.typ != TK_NUMBER && val.typ != TK_STRING {
		panic(fmt.Sprintf("expected value got %q", val.val))
	}
	return &Expr{col: col.val, op: op.typ, val: val.val}
}
 
func evalExpr(expr *Expr, row map[string]string) bool {
	rowVal, ok := row[expr.col]
	if !ok {
		return false
	}
	rowInt, rowIsInt := toInt(rowVal)
	exprInt, exprIsInt := toInt(expr.val)
	if rowIsInt && exprIsInt {
		switch expr.op {
		case TK_EQ:
			return rowInt == exprInt
		case TK_GT:
			return rowInt > exprInt
		case TK_LT:
			return rowInt < exprInt
		}
	}
	switch expr.op {
	case TK_EQ:
		return rowVal == expr.val
	}
	return false
}
 

func toInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}
