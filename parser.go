package main

import "fmt"

type ColType int

const (
	COL_INT ColType = iota
	COL_TEXT
)

func (t ColType) String() string {
	if t == COL_INT {
		return "INT"
	}
	return "TEXT"
}

type Column struct {
	Name string  `json:"name"`
	Type ColType `json:"type"`
}

type Literal struct {
	Val   string
	IsNum bool
}

type Cond struct {
	Col string
	Op  TokenType
	Lit Literal
}

type Assign struct {
	Col string
	Lit Literal
}

type Statement interface{ stmtNode() }

type CreateStmt struct {
	Table string
	Cols  []Column
}
type InsertStmt struct {
	Table string
	Vals  []Literal
}
type SelectStmt struct {
	Table string
	Cols  []string
	Where []Cond
}
type DeleteStmt struct {
	Table string
	Where []Cond
}
type UpdateStmt struct {
	Table string
	Sets  []Assign
	Where []Cond
}
type BeginStmt struct{}
type CommitStmt struct{}
type RollbackStmt struct{}

func (*CreateStmt) stmtNode()   {}
func (*InsertStmt) stmtNode()   {}
func (*SelectStmt) stmtNode()   {}
func (*DeleteStmt) stmtNode()   {}
func (*UpdateStmt) stmtNode()   {}
func (*BeginStmt) stmtNode()    {}
func (*CommitStmt) stmtNode()   {}
func (*RollbackStmt) stmtNode() {}

type syntaxError string

type Parser struct {
	toks []Token
	pos  int
}

func Parse(sql string) (stmt Statement, err error) {
	toks, err := newLexer(sql).tokenize()
	if err != nil {
		return nil, fmt.Errorf("syntax error: %w", err)
	}
	p := &Parser{toks: toks}
	defer func() {
		if r := recover(); r != nil {
			se, ok := r.(syntaxError)
			if !ok {
				panic(r)
			}
			stmt, err = nil, fmt.Errorf("syntax error: %s", string(se))
		}
	}()
	stmt = p.parseStatement()
	if p.peek().typ == TK_SEMI {
		p.pos++
	}
	if p.peek().typ != TK_EOF {
		p.fail("unexpected %s after end of statement", describe(p.peek()))
	}
	return stmt, nil
}

func describe(t Token) string {
	if t.typ == TK_EOF {
		return "end of input"
	}
	return fmt.Sprintf("%q", t.val)
}

func (p *Parser) fail(format string, args ...any) {
	panic(syntaxError(fmt.Sprintf(format, args...)))
}

func (p *Parser) peek() Token {
	if p.pos >= len(p.toks) {
		return Token{TK_EOF, ""}
	}
	return p.toks[p.pos]
}

func (p *Parser) next() Token {
	t := p.peek()
	p.pos++
	return t
}

func (p *Parser) expect(typ TokenType, what string) Token {
	t := p.next()
	if t.typ != typ {
		p.fail("expected %s, got %s", what, describe(t))
	}
	return t
}

func (p *Parser) parseStatement() Statement {
	switch p.peek().typ {
	case TK_CREATE:
		return p.parseCreate()
	case TK_INSERT:
		return p.parseInsert()
	case TK_SELECT:
		return p.parseSelect()
	case TK_UPDATE:
		return p.parseUpdate()
	case TK_DELETE:
		return p.parseDelete()
	case TK_BEGIN:
		p.next()
		return &BeginStmt{}
	case TK_COMMIT:
		p.next()
		return &CommitStmt{}
	case TK_ROLLBACK:
		p.next()
		return &RollbackStmt{}
	}
	p.fail("unexpected %s at start of statement", describe(p.peek()))
	return nil
}

func (p *Parser) parseCreate() *CreateStmt {
	p.expect(TK_CREATE, "CREATE")
	p.expect(TK_TABLE, "TABLE")
	name := p.expect(TK_IDENT, "table name")
	p.expect(TK_LPAREN, "'('")
	var cols []Column
	seen := map[string]bool{}
	for {
		cn := p.expect(TK_IDENT, "column name")
		tn := p.expect(TK_IDENT, "column type")
		var ct ColType
		switch tn.val {
		case "int", "integer", "bigint":
			ct = COL_INT
		case "text", "varchar", "string":
			ct = COL_TEXT
		default:
			p.fail("unknown column type %q (use INT or TEXT)", tn.val)
		}
		if seen[cn.val] {
			p.fail("duplicate column %q", cn.val)
		}
		seen[cn.val] = true
		cols = append(cols, Column{Name: cn.val, Type: ct})
		if p.peek().typ != TK_COMMA {
			break
		}
		p.next()
	}
	p.expect(TK_RPAREN, "')'")
	return &CreateStmt{Table: name.val, Cols: cols}
}

func (p *Parser) parseLiteral() Literal {
	t := p.next()
	switch t.typ {
	case TK_NUMBER:
		return Literal{Val: t.val, IsNum: true}
	case TK_STRING:
		return Literal{Val: t.val}
	}
	p.fail("expected a number or 'string', got %s", describe(t))
	return Literal{}
}

func (p *Parser) parseInsert() *InsertStmt {
	p.expect(TK_INSERT, "INSERT")
	p.expect(TK_INTO, "INTO")
	name := p.expect(TK_IDENT, "table name")
	p.expect(TK_VALUES, "VALUES")
	p.expect(TK_LPAREN, "'('")
	var vals []Literal
	for {
		vals = append(vals, p.parseLiteral())
		if p.peek().typ != TK_COMMA {
			break
		}
		p.next()
	}
	p.expect(TK_RPAREN, "')'")
	return &InsertStmt{Table: name.val, Vals: vals}
}

func (p *Parser) parseSelect() *SelectStmt {
	p.expect(TK_SELECT, "SELECT")
	var cols []string
	if p.peek().typ == TK_STAR {
		p.next()
		cols = []string{"*"}
	} else {
		for {
			cols = append(cols, p.expect(TK_IDENT, "column name").val)
			if p.peek().typ != TK_COMMA {
				break
			}
			p.next()
		}
	}
	p.expect(TK_FROM, "FROM")
	name := p.expect(TK_IDENT, "table name")
	return &SelectStmt{Table: name.val, Cols: cols, Where: p.parseOptionalWhere()}
}

func (p *Parser) parseUpdate() *UpdateStmt {
	p.expect(TK_UPDATE, "UPDATE")
	name := p.expect(TK_IDENT, "table name")
	p.expect(TK_SET, "SET")
	var sets []Assign
	for {
		col := p.expect(TK_IDENT, "column name")
		p.expect(TK_EQ, "'='")
		sets = append(sets, Assign{Col: col.val, Lit: p.parseLiteral()})
		if p.peek().typ != TK_COMMA {
			break
		}
		p.next()
	}
	return &UpdateStmt{Table: name.val, Sets: sets, Where: p.parseOptionalWhere()}
}

func (p *Parser) parseDelete() *DeleteStmt {
	p.expect(TK_DELETE, "DELETE")
	p.expect(TK_FROM, "FROM")
	name := p.expect(TK_IDENT, "table name")
	return &DeleteStmt{Table: name.val, Where: p.parseOptionalWhere()}
}

func (p *Parser) parseOptionalWhere() []Cond {
	if p.peek().typ != TK_WHERE {
		return nil
	}
	p.next()
	var conds []Cond
	for {
		col := p.expect(TK_IDENT, "column name")
		op := p.next()
		switch op.typ {
		case TK_EQ, TK_NE, TK_LT, TK_LE, TK_GT, TK_GE:
		default:
			p.fail("expected a comparison operator, got %s", describe(op))
		}
		conds = append(conds, Cond{Col: col.val, Op: op.typ, Lit: p.parseLiteral()})
		if p.peek().typ != TK_AND {
			break
		}
		p.next()
	}
	return conds
}
