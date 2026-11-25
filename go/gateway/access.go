// Project access providers (docs/designs/fleet-project-access.md#how-agents-use-a-grant): a connection whose provider
// names a transport is served here, not over HTTP. The gateway logs in to the project's server as the grant's OS user
// (through the jump server when there is one, every host key pinned), with the key and password files under the access
// directory, and answers mysql reads (sql_query, sql_schema) and file reads (files_list, files_get over SFTP). Every
// call's spool line carries what it read (detail): the audit of each query and file.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/ssh"

	"fleet/contract"
)

const accessJumpUser = "fleet-jump"

// accessDir: <FLEET_SECRET_DIR>/access, the sibling of the connection keys directory.
func (g *gateway) accessDir() string {
	return filepath.Join(filepath.Dir(filepath.Clean(g.config.KeysDir)), "access")
}

// accessLink: one connection's login on its server, kept while the connection's access stays the same.
type accessLink struct {
	ident  string
	client *ssh.Client
	closer []io.Closer
	db     *sql.DB
}

func (l *accessLink) close() {
	if l.db != nil {
		_ = l.db.Close()
	}
	for i := len(l.closer) - 1; i >= 0; i-- {
		_ = l.closer[i].Close()
	}
}

type accessLinks struct {
	mu    sync.Mutex
	links map[string]*accessLink
}

var accessDialers sync.Map // mysql net name -> registered

func accessIdent(a *contract.GatewayAccess) string {
	b, _ := json.Marshal(a)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// link answers the connection's login, dialing it when missing or when its access changed.
func (g *gateway) link(ctx context.Context, name string, a *contract.GatewayAccess) (*accessLink, error) {
	ident := accessIdent(a)
	g.access.mu.Lock()
	defer g.access.mu.Unlock()
	if g.access.links == nil {
		g.access.links = map[string]*accessLink{}
	}
	if l := g.access.links[name]; l != nil {
		if l.ident == ident {
			return l, nil
		}
		l.close()
		delete(g.access.links, name)
	}
	l, err := g.dialAccess(ctx, a)
	if err != nil {
		return nil, err
	}
	l.ident = ident
	g.access.links[name] = l
	return l, nil
}

// drop forgets a login that failed, so the next call dials again.
func (g *gateway) drop(name string, l *accessLink) {
	g.access.mu.Lock()
	defer g.access.mu.Unlock()
	if g.access.links[name] == l {
		delete(g.access.links, name)
	}
	l.close()
}

func (g *gateway) dialAccess(ctx context.Context, a *contract.GatewayAccess) (*accessLink, error) {
	if len(a.Hops) == 0 {
		return nil, errors.New("the access has no server")
	}
	raw, err := os.ReadFile(filepath.Join(g.accessDir(), filepath.Clean("/"+a.Key)))
	if err != nil {
		return nil, fmt.Errorf("the grant's key is missing on the gateway host")
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("the grant's key is unreadable")
	}
	l := &accessLink{}
	var through *ssh.Client
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for i, h := range a.Hops {
		addr := h.Address
		if _, _, err := net.SplitHostPort(addr); err != nil {
			addr = net.JoinHostPort(addr, "22")
		}
		var c net.Conn
		if through == nil {
			c, err = (&net.Dialer{Timeout: 20 * time.Second}).DialContext(dctx, "tcp", addr)
		} else {
			c, err = through.DialContext(dctx, "tcp", addr)
		}
		if err != nil {
			l.close()
			return nil, fmt.Errorf("reach %s: %w", addr, err)
		}
		l.closer = append(l.closer, c)
		user := accessJumpUser
		if i == len(a.Hops)-1 {
			user = a.User
		}
		pinned := strings.TrimSpace(h.HostKey)
		cfg := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 20 * time.Second,
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				if strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) != pinned {
					return fmt.Errorf("host key of %s is not the pinned one", addr)
				}
				return nil
			}}
		if dl, ok := dctx.Deadline(); ok && through == nil {
			_ = c.SetDeadline(dl)
		}
		conn, chans, reqs, err := ssh.NewClientConn(c, addr, cfg)
		if err != nil {
			l.close()
			return nil, fmt.Errorf("log in to %s as %s: %w", addr, user, err)
		}
		if through == nil {
			_ = c.SetDeadline(time.Time{})
		}
		through = ssh.NewClient(conn, chans, reqs)
		l.closer = append(l.closer, through)
	}
	l.client = through
	return l, nil
}

// accessDB: the grant's MySQL pool over the login's forwarding (the server's 127.0.0.1:3306).
func (g *gateway) accessDB(name string, l *accessLink, a *contract.GatewayAccess) (*sql.DB, error) {
	if l.db != nil {
		return l.db, nil
	}
	if a.Password == nil || a.DbUser == nil || a.Database == nil {
		return nil, errors.New("the grant names no database user")
	}
	pw, err := os.ReadFile(filepath.Join(g.accessDir(), filepath.Clean("/"+*a.Password)))
	if err != nil {
		return nil, errors.New("the grant's database password is missing on the gateway host")
	}
	netName := "fleet-ssh-" + name
	if _, loaded := accessDialers.LoadOrStore(netName, true); !loaded {
		mysql.RegisterDialContext(netName, func(ctx context.Context, addr string) (net.Conn, error) {
			g.access.mu.Lock()
			cur := g.access.links[name]
			g.access.mu.Unlock()
			if cur == nil {
				return nil, errors.New("no login for " + name)
			}
			return cur.client.DialContext(ctx, "tcp", addr)
		})
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = *a.DbUser, strings.TrimSpace(string(pw)), netName, "127.0.0.1:3306", *a.Database
	cfg.ParseTime = false
	cfg.InterpolateParams = false
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	l.db = sql.OpenDB(connector)
	l.db.SetConnMaxIdleTime(5 * time.Minute)
	return l.db, nil
}

// accessArgs: the arguments of a project access tool.
type accessArgs struct {
	SQL     string `json:"sql"`
	MaxRows int64  `json:"maxRows"`
	Path    string `json:"path"`
}

var readStatement = regexp.MustCompile(`(?i)^(select|show|describe|desc|explain|with)\b`)

// sqlText: one statement without a trailing semicolon; for read, it must start with a read keyword (the database
// user's grants refuse the rest anyway).
func sqlText(text string, level contract.GatewayAccessLevel) (string, error) {
	s := strings.TrimSpace(text)
	for strings.HasPrefix(s, "--") || strings.HasPrefix(s, "/*") || strings.HasPrefix(s, "#") {
		if strings.HasPrefix(s, "/*") {
			end := strings.Index(s, "*/")
			if end < 0 {
				return "", errors.New("unclosed comment")
			}
			s = strings.TrimSpace(s[end+2:])
			continue
		}
		nl := strings.IndexByte(s, '\n')
		if nl < 0 {
			return "", errors.New("the query is only a comment")
		}
		s = strings.TrimSpace(s[nl+1:])
	}
	s = strings.TrimSpace(strings.TrimRight(s, "; \n\t"))
	if s == "" {
		return "", errors.New("sql is empty")
	}
	if strings.Contains(s, ";") {
		return "", errors.New("one statement per call (a ; inside a string literal counts too)")
	}
	if level == contract.GatewayAccessLevelRead && !readStatement.MatchString(s) {
		return "", errors.New("a read grant runs SELECT, SHOW, DESCRIBE, EXPLAIN or WITH only")
	}
	return s, nil
}

// sqlReply: a query's answer: columns, rows as text (NULL is null), and whether rows were left out.
type sqlReply struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"rowCount"`
	Truncated bool     `json:"truncated,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (g *gateway) runQuery(ctx context.Context, db *sql.DB, a *contract.GatewayAccess, text string, maxRows int64, cap int64) (sqlReply, error) {
	out := sqlReply{Columns: []string{}, Rows: [][]any{}}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: a.Level == contract.GatewayAccessLevelRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, text)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	if out.Columns, err = rows.Columns(); err != nil {
		return out, err
	}
	size := int64(0)
	for rows.Next() {
		vals := make([]any, len(out.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return out, err
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
			case []byte:
				if utf8.Valid(x) {
					row[i] = string(x)
				} else {
					row[i] = "base64:" + b64(x)
				}
			default:
				row[i] = x
			}
		}
		b, _ := json.Marshal(row)
		if (maxRows > 0 && int64(len(out.Rows)) >= maxRows) || size+int64(len(b)) > cap {
			out.Truncated = true
			out.Note = fmt.Sprintf("stopped after %d rows: maxRows or the reply size limit of %d bytes; narrow the query or page with LIMIT/OFFSET", len(out.Rows), cap)
			break
		}
		size += int64(len(b)) + 1
		out.Rows = append(out.Rows, row)
	}
	out.RowCount = len(out.Rows)
	return out, rows.Err()
}

// serveAccess answers a call of a transport connection; nil means a download streamed.
func (g *gateway) serveAccess(w httpWriter, ctx context.Context, q *request, args map[string]json.RawMessage, accept string) *answer {
	a := q.c.Access
	if a == nil {
		return refusal(503, "error", "access-missing", "the connection has no grant on the gateway")
	}
	var in accessArgs
	raw, _ := json.Marshal(args)
	if err := json.Unmarshal(raw, &in); err != nil {
		return refusal(400, "error", "bad-request", "arguments: "+err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, g.config.requestTimeout*4)
	defer cancel()
	l, err := g.link(ctx, q.connection, a)
	if err != nil {
		return refusal(502, "error", "access-unreachable", err.Error())
	}
	ok := func(v any, detail string) *answer {
		body, _ := json.Marshal(v)
		return &answer{status: 200, body: body, outcome: "ok", detail: detail}
	}
	failed := func(code string, err error, detail string) *answer {
		a := refusal(400, "error", code, err.Error())
		a.detail = detail
		return a
	}
	_, replyCap, _, _ := limits(q.c)
	if replyCap <= 0 || replyCap > providerBytes {
		replyCap = providerBytes
	}
	switch *q.p.Transport {
	case contract.GatewayTransportMysql:
		db, err := g.accessDB(q.connection, l, a)
		if err != nil {
			return refusal(502, "error", "access-unreachable", err.Error())
		}
		text := ""
		switch q.tool {
		case "sql_query":
			if text, err = sqlText(in.SQL, a.Level); err != nil {
				return failed("bad-query", err, "sql: "+in.SQL)
			}
		case "sql_schema":
			text = "SELECT table_name, column_name, column_type, column_key, is_nullable FROM information_schema.columns WHERE table_schema = DATABASE() ORDER BY table_name, ordinal_position"
		default:
			return refusal(404, "error", "unknown-tool", "the connection has no such tool")
		}
		out, err := g.runQuery(ctx, db, a, text, in.MaxRows, replyCap-1024)
		detail := fmt.Sprintf("sql: %s; rows: %d", text, out.RowCount)
		if err != nil {
			var my *mysql.MySQLError
			if errors.As(err, &my) {
				return failed("query-refused", err, detail)
			}
			g.drop(q.connection, l)
			return failed("access-failed", err, detail)
		}
		return ok(out, detail)
	case contract.GatewayTransportFiles:
		target, err := chrootPath(a.Paths, in.Path)
		if err != nil {
			return failed("bad-path", err, "path: "+in.Path)
		}
		s, err := openSFTP(l.client)
		if err != nil {
			g.drop(q.connection, l)
			return refusal(502, "error", "access-unreachable", err.Error())
		}
		defer s.close()
		switch q.tool {
		case "files_list":
			entries, err := s.list(target)
			if err != nil {
				return failed("file-refused", err, "list: "+target)
			}
			return ok(struct {
				Path    string      `json:"path"`
				Entries []fileEntry `json:"entries"`
			}{target, entries}, fmt.Sprintf("list: %s; entries: %d", target, len(entries)))
		case "files_get":
			if !strings.Contains(accept, "application/octet-stream") {
				return refusal(400, "error", "file-expected", "a download tool answers file bytes; send Accept: application/octet-stream")
			}
			return g.streamSFTP(w, q, s, target, replyCap)
		}
		return refusal(404, "error", "unknown-tool", "the connection has no such tool")
	}
	return refusal(404, "error", "unknown-tool", "unknown transport")
}

// chrootPath maps a path the agent gives (a server path under a granted folder, or /<folder>/... inside the grant's
// chroot) to the chroot path; anything else is refused.
func chrootPath(granted []string, given string) (string, error) {
	p := path.Clean("/" + strings.TrimSpace(given))
	if strings.Contains(given, "..") {
		return "", errors.New("path may not contain ..")
	}
	if p == "/" {
		return "/", nil
	}
	for _, g := range granted {
		g = path.Clean(g)
		base := "/" + path.Base(g)
		switch {
		case p == g || strings.HasPrefix(p, g+"/"):
			return base + strings.TrimPrefix(p, g), nil
		case p == base || strings.HasPrefix(p, base+"/"):
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is outside the granted folders %s", given, strings.Join(granted, ", "))
}
