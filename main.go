// goip-bridge — standalone GoIP SMS/USSD gateway.
//
// One file, Go + a MySQL driver. Speaks the GoIP "SMS server" UDP protocol
// directly with the hardware and integrates with MySQL: inbound SMS are written
// to an inbox table, outbound SMS are read from an outbox queue and their status
// (sent / delivered / failed) is written back. A small HTTP API (USSD, direct
// send, lines, inbox) and an optional webhook are also provided.
//
// Build:  go build -o goip-bridge .
// Run:    ./goip-bridge -config config.json
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// Build/identity metadata. appVersion can be overridden at build time:
//   go build -ldflags "-X main.appVersion=0.3.0" .
const (
	appName      = "goip-bridge"
	appTagline   = "GoIP SMS/USSD gateway"
	appCopyright = "Copyright (c) 2026 Evgenii Shapovalov"
)

var appVersion = "0.3.0"

// ----------------------------------------------------------------------------
// Config
// ----------------------------------------------------------------------------

type DBConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	Password    string `json:"password"`
	Name        string `json:"name"`
	InboxTable  string `json:"inbox_table"`
	OutboxTable string `json:"outbox_table"`
	PollSec     int    `json:"poll_sec"`
}

type Config struct {
	ListenUDP      string            `json:"listen_udp"`      // device-facing, e.g. "172.16.172.3:44444"
	ListenHTTP     string            `json:"listen_http"`     // API, e.g. "127.0.0.1:8080"
	HTTPToken      string            `json:"http_token"`      // Bearer token for the API ("" = open)
	WebhookURL     string            `json:"webhook_url"`     // optional POST of inbound SMS + DLR ("" = off)
	WebhookToken   string            `json:"webhook_token"`   // sent as Bearer to the webhook
	SendTimeout    int               `json:"send_timeout_sec"`
	USSDTimeout    int               `json:"ussd_timeout_sec"`
	USSDRetransmit int               `json:"ussd_retransmit_sec"`
	Debug          bool              `json:"debug"`               // detailed SMS/USSD/inbound logging to file
	LogMaxMB       int               `json:"log_max_mb"`          // per-file log cap in MB (default 10)
	LineDeadSec    int               `json:"line_dead_after_sec"` // line treated as dead after this many seconds without keepalive (default 120)
	AllowSrc       []string          `json:"allow_src"`           // optional CIDR/IP allow-list for device UDP packets (empty = accept all; firewall is then the only barrier)
	DB             *DBConfig         `json:"db"`                  // optional MySQL integration (absent = off)
	LinePasswords  map[string]string `json:"line_passwords"`      // optional per-line password override; else learned from keepalive

	allowNets []*net.IPNet // parsed AllowSrc (populated by loadConfig)
}

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validIdent(s string) bool { return len(s) <= 64 && identRE.MatchString(s) }

var numberRE = regexp.MustCompile(`^\+?[0-9]{3,20}$`)

// validNumber accepts an MSISDN: optional leading +, then 3..20 digits and nothing else
// (rejects spaces/letters that would otherwise inject extra tokens into the SEND command).
func validNumber(s string) bool { return numberRE.MatchString(s) }

// parseAllowSrc turns the allow_src list (CIDRs or bare IPs) into networks for source filtering.
func parseAllowSrc(list []string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			if strings.Contains(s, ":") {
				s += "/128"
			} else {
				s += "/32"
			}
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("allow_src %q: %w", s, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.ListenUDP == "" {
		c.ListenUDP = ":44444"
	}
	if c.ListenHTTP == "" {
		c.ListenHTTP = "127.0.0.1:8080"
	}
	if c.SendTimeout == 0 {
		c.SendTimeout = 45
	}
	if c.USSDTimeout == 0 {
		c.USSDTimeout = 120
	}
	if c.USSDRetransmit == 0 {
		c.USSDRetransmit = 60
	}
	if c.LogMaxMB == 0 {
		c.LogMaxMB = 10
	}
	if c.LineDeadSec == 0 {
		c.LineDeadSec = 120
	}
	if c.DB != nil {
		if c.DB.Host == "" {
			c.DB.Host = "127.0.0.1"
		}
		if c.DB.Port == 0 {
			c.DB.Port = 3306
		}
		if c.DB.InboxTable == "" {
			c.DB.InboxTable = "goip_inbox"
		}
		if c.DB.OutboxTable == "" {
			c.DB.OutboxTable = "goip_outbox"
		}
		if c.DB.PollSec == 0 {
			c.DB.PollSec = 2
		}
		// Table names are interpolated into SQL (drivers can't parameterize identifiers),
		// so validate them as plain identifiers even though config is trusted.
		if !validIdent(c.DB.InboxTable) || !validIdent(c.DB.OutboxTable) {
			return nil, fmt.Errorf("invalid db table name (must match %s)", identRE.String())
		}
	}
	if c.LinePasswords == nil {
		c.LinePasswords = map[string]string{}
	}
	nets, err := parseAllowSrc(c.AllowSrc)
	if err != nil {
		return nil, err
	}
	c.allowNets = nets
	return c, nil
}

// ----------------------------------------------------------------------------
// Size-capped log writer (rotates to "<path>.1" so the active file stays <= cap)
// ----------------------------------------------------------------------------

type cappedWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

func newCappedWriter(path string, maxBytes int64) *cappedWriter {
	cw := &cappedWriter{path: path, maxBytes: maxBytes}
	cw.reopen()
	return cw
}

func (cw *cappedWriter) reopen() {
	f, err := os.OpenFile(cw.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // logs hold phone numbers + SMS text
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: cannot open log %s: %v\n", cw.path, err)
		return
	}
	cw.f = f
	cw.size = 0
	if st, err := f.Stat(); err == nil {
		cw.size = st.Size()
	}
}

func (cw *cappedWriter) Write(p []byte) (int, error) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.f == nil {
		return len(p), nil
	}
	if cw.size > 0 && cw.size+int64(len(p)) > cw.maxBytes {
		cw.f.Close()
		os.Rename(cw.path, cw.path+".1") // keep one previous file; newest lines stay at the bottom
		cw.reopen()
	}
	n, err := cw.f.Write(p)
	cw.size += int64(n)
	return n, err
}

// ----------------------------------------------------------------------------
// Line registry
// ----------------------------------------------------------------------------

type Line struct {
	ID        string       `json:"id"`
	Password  string       `json:"-"`
	Addr      *net.UDPAddr `json:"-"`
	AddrStr   string       `json:"addr"`
	Num       string       `json:"num"`
	Signal    int          `json:"signal"`
	GSMStatus string       `json:"gsm_status"`
	IMEI      string       `json:"imei"`
	IMSI      string       `json:"imsi"`
	ICCID     string       `json:"iccid"`
	Carrier   string       `json:"carrier"`
	Alive     bool         `json:"alive"`
	LastSeen  time.Time    `json:"last_seen"`
}

type Inbound struct {
	Line string    `json:"line"`
	From string    `json:"from"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// ----------------------------------------------------------------------------
// Server
// ----------------------------------------------------------------------------

type Server struct {
	cfg  *Config
	conn *net.UDPConn
	dbp  atomic.Pointer[sql.DB] // nil until/unless MySQL is connected
	srv  *http.Server
	elog *log.Logger // errors -> goip-bridge.err.log (+ main + stderr)

	mu    sync.RWMutex
	lines map[string]*Line

	sessions sync.Map // sessionID(string) -> chan string

	seq uint64 // monotonic counter for session/ref ids

	inboxMu sync.Mutex
	inbox   []Inbound // ring buffer of recent inbound messages

	seenMu sync.Mutex
	seen   map[string]time.Time // dedup of inbound RECEIVE/DELIVER (key = "verb:line:seq")

	webhookSem chan struct{}
	httpClient *http.Client

	inflight sync.WaitGroup // in-flight sends, drained on shutdown so status reaches the DB
	drainMu  sync.RWMutex   // guards `draining`; held (R) across inflight.Add so Add never races Wait
	draining bool           // set true at shutdown; blocks new sends from registering
	fbMu     sync.Mutex
	fbPath   string // goip-bridge.fallback.jsonl — durable record of DB writes that exhausted retries
}

const inboxCap = 500

func newServer(cfg *Config) *Server {
	return &Server{
		cfg:        cfg,
		lines:      map[string]*Line{},
		seen:       map[string]time.Time{},
		seq:        uint64(time.Now().Unix()),
		elog:       log.New(os.Stderr, "", log.LstdFlags), // replaced in main once log files are open
		webhookSem: make(chan struct{}, 16),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *Server) DB() *sql.DB     { return s.dbp.Load() }
func (s *Server) nextID() string  { return strconv.FormatUint(atomic.AddUint64(&s.seq, 1), 10) }
func (s *Server) outbox() string  { return s.cfg.DB.OutboxTable }

// dbg logs detailed SMS/USSD/inbound activity only when debug is enabled (never keepalive).
func (s *Server) dbg(format string, a ...any) {
	if s.cfg.Debug {
		log.Printf("[dbg] "+format, a...)
	}
}

// beginSend registers an in-flight send unless shutdown has begun. False means we're draining:
// the caller must return immediately and must NOT call inflight.Done. The RLock pairs with the
// Lock in main so an Add can never start after Wait has begun (avoids a WaitGroup panic).
func (s *Server) beginSend() bool {
	s.drainMu.RLock()
	defer s.drainMu.RUnlock()
	if s.draining {
		return false
	}
	s.inflight.Add(1)
	return true
}

// allowedSrc reports whether a device packet from addr is accepted. With no allow_src configured
// every source is accepted (the firewall is then the only barrier).
func (s *Server) allowedSrc(addr *net.UDPAddr) bool {
	if len(s.cfg.allowNets) == 0 {
		return true
	}
	if addr == nil {
		return false
	}
	for _, n := range s.cfg.allowNets {
		if n.Contains(addr.IP) {
			return true
		}
	}
	return false
}

// lineAlive reports whether a line is usable: GSM-registered AND seen via keepalive recently.
func (s *Server) lineAlive(ln *Line) bool {
	return ln.Alive && time.Since(ln.LastSeen) < time.Duration(s.cfg.LineDeadSec)*time.Second
}

// sendTo writes a raw protocol line to a device address.
func (s *Server) sendTo(addr *net.UDPAddr, msg string) {
	if addr == nil {
		return
	}
	if _, err := s.conn.WriteToUDP([]byte(msg), addr); err != nil {
		s.elog.Printf("udp write to %s failed: %v", addr, err)
	}
}

// seenRecently reports whether key was already recorded (and records it now). Entries are kept
// until the map grows past 8192, after which those older than 10 minutes are purged — so the
// practical dedup window is "at least 10 minutes" (effectively longer at low traffic).
// Used to drop retransmitted RECEIVE/DELIVER packets (the device repeats them if our ack is lost).
func (s *Server) seenRecently(key string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	now := time.Now()
	_, dup := s.seen[key]
	s.seen[key] = now
	if len(s.seen) > 8192 {
		for k, t := range s.seen {
			if now.Sub(t) > 10*time.Minute {
				delete(s.seen, k)
			}
		}
	}
	return dup
}

// ----------------------------------------------------------------------------
// UDP receive loop + dispatch
// ----------------------------------------------------------------------------

func (s *Server) udpLoop(ctx context.Context) {
	buf := make([]byte, 65536)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			s.elog.Printf("udp read error: %v", err)
			continue
		}
		s.safeDispatch(string(buf[:n]), addr)
	}
}

// safeDispatch isolates a panic from a malformed packet so it can't kill the read loop.
func (s *Server) safeDispatch(payload string, addr *net.UDPAddr) {
	defer func() {
		if r := recover(); r != nil {
			s.elog.Printf("dispatch panic (%q): %v", payload, r)
		}
	}()
	s.dispatch(payload, addr)
}

func (s *Server) dispatch(payload string, addr *net.UDPAddr) {
	if !s.allowedSrc(addr) {
		s.dbg("dropped packet from disallowed src %s: %q", addr, payload)
		return
	}
	p := strings.TrimRight(payload, "\r\n")
	switch {
	case strings.HasPrefix(p, "req:"):
		s.handleKeepalive(p, addr) // never logged (keepalive == ping)
	case isColonEvent(p):
		verb, seq, _ := splitColonEvent(p)
		switch verb {
		case "RECEIVE":
			s.handleReceive(p, seq, addr)
		case "DELIVER":
			s.handleDeliver(p, seq, addr)
		default:
			// CELLINFO / BCCH / CGATT / ... — device status events; ack and (debug) note them.
			s.dbg("event %s from %s: %q", verb, addr, p)
			s.sendTo(addr, fmt.Sprintf("%s %s OK", verb, seq))
		}
	default:
		// Space-delimited response for an in-flight send/USSD session. Non-blocking send is
		// intentional: udpLoop is a single goroutine and must never block on a finished session.
		tok := strings.Fields(p)
		if len(tok) >= 2 {
			if ch, ok := s.sessions.Load(tok[1]); ok {
				select {
				case ch.(chan string) <- p:
				default:
					s.elog.Printf("session %s channel full, dropped: %q", tok[1], p)
				}
				return
			}
		}
		s.dbg("unrouted packet from %s: %q", addr, p)
	}
}

func isColonEvent(p string) bool {
	i := strings.IndexByte(p, ':')
	if i <= 0 {
		return false
	}
	for _, r := range p[:i] {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_') {
			return false
		}
	}
	return true
}

func splitColonEvent(p string) (verb, seq, rest string) {
	i := strings.IndexByte(p, ':')
	verb = p[:i]
	rest = p[i+1:]
	if j := strings.IndexByte(rest, ';'); j >= 0 {
		seq = rest[:j]
		rest = rest[j+1:]
	} else {
		seq = rest
		rest = ""
	}
	return
}

// fields parses a ";"-separated key:value list, treating everything after the
// first "msg:" as the raw message body (it may contain ; and :).
func fields(s string) (map[string]string, string) {
	m := map[string]string{}
	body := ""
	if idx := strings.Index(s, "msg:"); idx >= 0 {
		body = s[idx+len("msg:"):]
		s = s[:idx]
	}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if j := strings.IndexByte(part, ':'); j >= 0 {
			m[part[:j]] = part[j+1:]
		}
	}
	return m, body
}

// ----------------------------------------------------------------------------
// Keepalive
// ----------------------------------------------------------------------------

func (s *Server) handleKeepalive(p string, addr *net.UDPAddr) {
	_, seq, rest := splitColonEvent(p)
	f, _ := fields(rest)
	id := f["id"]
	if id == "" {
		return
	}
	s.mu.Lock()
	ln := s.lines[id]
	if ln == nil {
		ln = &Line{ID: id}
		s.lines[id] = ln
	}
	ln.Addr = addr
	ln.AddrStr = addr.String()
	if pw, ok := s.cfg.LinePasswords[id]; ok {
		ln.Password = pw
	} else if f["pass"] != "" {
		ln.Password = f["pass"]
	} else if f["password"] != "" {
		ln.Password = f["password"]
	}
	ln.Num = f["num"]
	ln.Signal = atoi(f["signal"])
	ln.GSMStatus = f["gsm_status"]
	ln.IMEI = f["imei"]
	ln.IMSI = f["imsi"]
	ln.ICCID = f["iccid"]
	if f["pro"] != "" {
		ln.Carrier = f["pro"]
	}
	ln.Alive = f["gsm_status"] == "LOGIN"
	ln.LastSeen = time.Now()
	s.mu.Unlock()

	s.sendTo(addr, fmt.Sprintf("reg:%s;status:200;v:1;", seq))
}

// ----------------------------------------------------------------------------
// Inbound SMS + delivery reports
// ----------------------------------------------------------------------------

func (s *Server) handleReceive(p, seq string, addr *net.UDPAddr) {
	// RECEIVE:<n>;id:<line>;password:<pwd>;srcnum:<from>;msg:<text>
	_, _, rest := splitColonEvent(p)
	f, body := fields(rest)
	from := first(f, "srcnum", "num", "src", "sender")
	line := f["id"]
	s.sendTo(addr, fmt.Sprintf("RECEIVE %s OK", seq)) // ack first so the device stops retransmitting
	if s.cfg.Debug { // flag inbound arriving from an IP that doesn't match the line's keepalive source
		s.mu.RLock()
		ln := s.lines[line]
		s.mu.RUnlock()
		if ln != nil && ln.Addr != nil && !ln.Addr.IP.Equal(addr.IP) {
			s.dbg("RX line=%s from unexpected ip %s (registered %s)", line, addr.IP, ln.Addr.IP)
		}
	}

	// Drop retransmits (device repeats RECEIVE:<n> if our ack was lost) — else we'd double-store.
	if s.seenRecently("R:" + line + ":" + seq) {
		s.dbg("RX dup ignored line=%s seq=%s", line, seq)
		return
	}

	in := Inbound{Line: line, From: from, Text: body, Time: time.Now()}
	s.storeInbox(in)
	log.Printf("RECV line=%s from=%s len=%d", line, from, len(body))
	s.dbg("RX line=%s from=%s text=%q", line, from, body)

	if s.DB() != nil {
		go s.insertInbox(line, from, body)
	}
	if s.cfg.WebhookURL != "" {
		s.fireWebhook(map[string]any{"type": "sms", "line": line, "from": from, "text": body, "time": in.Time})
	}
}

func (s *Server) insertInbox(line, from, body string) {
	q := "INSERT INTO " + s.cfg.DB.InboxTable + " (line, from_number, text, received_at) VALUES (?,?,?,NOW())"
	for i := 0; i < 3; i++ {
		db := s.DB()
		if db == nil {
			break
		}
		if _, err := db.Exec(q, line, from, body); err == nil {
			return
		} else {
			s.elog.Printf("inbox insert (try %d): %v", i+1, err)
		}
		time.Sleep(2 * time.Second)
	}
	s.elog.Printf("CRIT: inbox insert failed (saved to fallback): line=%s from=%s", line, from)
	s.appendFallback(map[string]any{"kind": "inbox", "line": line, "from": from, "text": body, "ts": time.Now().Format(time.RFC3339)})
}

func (s *Server) handleDeliver(p, seq string, addr *net.UDPAddr) {
	// DELIVER:<n>;id:<line>;password:<pwd>;sms_no:<k>;state:<s>   (state 0 = delivered)
	_, _, rest := splitColonEvent(p)
	f, _ := fields(rest)
	s.sendTo(addr, fmt.Sprintf("DELIVER %s OK", seq))
	line, smsNo, state := f["id"], f["sms_no"], f["state"]
	log.Printf("DLR line=%s sms_no=%s state=%s", line, smsNo, state)

	if s.seenRecently("D:" + line + ":" + seq) {
		return
	}
	if s.DB() != nil && smsNo != "" {
		go s.applyDLR(line, smsNo, state)
	}
	if s.cfg.WebhookURL != "" {
		s.fireWebhook(map[string]any{"type": "dlr", "line": line, "sms_no": smsNo, "state": state, "time": time.Now()})
	}
}

// applyDLR marks the matching outbox row delivered/failed. A DLR can arrive in the same
// instant the send's 'sent'+sms_no row is committed, so retry until the row is there.
// The 15-min window guards against a wrapped/reused sms_no matching an old row.
func (s *Server) applyDLR(line, smsNo, state string) {
	base := " WHERE line=? AND sms_no=? AND status='sent' AND sent_at >= NOW() - INTERVAL 15 MINUTE ORDER BY id DESC LIMIT 1"
	for i := 0; i < 6; i++ {
		if db := s.DB(); db != nil {
			var res sql.Result
			var err error
			if state == "0" {
				res, err = db.Exec("UPDATE "+s.outbox()+" SET status='delivered', delivered_at=NOW()"+base, line, smsNo)
			} else {
				res, err = db.Exec("UPDATE "+s.outbox()+" SET status='failed', error_code=?, delivered_at=NOW()"+base, "dlr_state:"+state, line, smsNo)
			}
			if err == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					return
				}
			} else {
				s.elog.Printf("dlr update: %v", err)
			}
		}
		time.Sleep(1500 * time.Millisecond)
	}
	// The matching 'sent' row never appeared (send failed/unrecorded, or DB was down) — don't drop
	// the DLR silently; record it next to the config for manual reconciliation.
	s.elog.Printf("CRIT: dlr unmatched after retries (saved to fallback): line=%s sms_no=%s state=%s", line, smsNo, state)
	s.appendFallback(map[string]any{"kind": "dlr", "line": line, "sms_no": smsNo, "state": state, "ts": time.Now().Format(time.RFC3339)})
}

func (s *Server) storeInbox(in Inbound) {
	s.inboxMu.Lock()
	defer s.inboxMu.Unlock()
	s.inbox = append(s.inbox, in)
	if len(s.inbox) > inboxCap {
		s.inbox = s.inbox[len(s.inbox)-inboxCap:]
	}
}

// ----------------------------------------------------------------------------
// Outbound: SMS send
// ----------------------------------------------------------------------------

// sendSMS drives one send and returns (ok, sms_no, detail). `line` must be a snapshot copy.
//
// Protocol: MSG -> dev:PASSWORD -> PASSWORD pass -> dev:SEND -> SEND ref num ->
//           dev:WAIT -> dev:OK <id> <ref> <sms_no> (sent) | ERROR <id> <ref> errorstatus:<n>.
// Never re-send SEND while WAITing — the device performs a fresh send per SEND (duplicates).
func (s *Server) sendSMS(line *Line, number, text string) (bool, string, string) {
	if !s.beginSend() {
		return false, "", "shutting_down"
	}
	defer s.inflight.Done()
	if line.Addr == nil {
		return false, "", "no_address"
	}
	number = strings.TrimSpace(number)
	if !validNumber(number) { // reject spaces/junk that would inject extra tokens into SEND (text is left intact)
		s.dbg("TX line=%s rejected bad number %q", line.ID, number)
		return false, "", "bad_number"
	}
	id := s.nextID()
	ref := s.nextID()
	ch := make(chan string, 64)
	s.sessions.Store(id, ch)
	defer s.sessions.Delete(id)

	addr := line.Addr
	send := func(m string) { s.sendTo(addr, m) }
	s.dbg("TX line=%s to=%s len=%d text=%q", line.ID, number, len(text), text)

	send(fmt.Sprintf("MSG %s %d %s\n", id, len(text), text))
	submitted := false
	overall := time.NewTimer(time.Duration(s.cfg.SendTimeout) * time.Second)
	defer overall.Stop()
	for {
		select {
		case <-overall.C:
			send(fmt.Sprintf("DONE %s\n", id))
			s.dbg("TX line=%s to=%s -> timeout", line.ID, number)
			return false, "", "timeout"
		case resp := <-ch:
			tok := strings.Fields(resp)
			if len(tok) == 0 {
				continue
			}
			switch tok[0] {
			case "PASSWORD":
				send(fmt.Sprintf("PASSWORD %s %s\n", id, line.Password))
			case "SEND":
				if !submitted {
					send(fmt.Sprintf("SEND %s %s %s\n", id, ref, number))
					submitted = true
				}
			case "WAIT":
				// queued / sending — wait for OK or ERROR
			case "OK":
				send(fmt.Sprintf("DONE %s\n", id))
				smsNo := ""
				if len(tok) >= 4 {
					smsNo = tok[3]
				}
				s.dbg("TX line=%s to=%s -> sent sms_no=%s", line.ID, number, smsNo)
				return true, smsNo, ""
			case "ERROR":
				send(fmt.Sprintf("DONE %s\n", id))
				detail := "error"
				if parts := strings.SplitN(resp, " ", 3); len(parts) >= 3 {
					detail = parts[2] // "<ref> errorstatus:<n>" or "PASSWORD"
				}
				s.dbg("TX line=%s to=%s -> failed %s", line.ID, number, detail)
				return false, "", detail
			}
		}
	}
}

// ----------------------------------------------------------------------------
// Outbound: USSD  ("USSD <id> <pass> <code>" -> "USSD <id> <reply>"); no trailing newline.
// ----------------------------------------------------------------------------

func (s *Server) sendUSSD(line *Line, code string) (string, error) {
	if !s.beginSend() {
		return "", fmt.Errorf("shutting down")
	}
	defer s.inflight.Done()
	if line.Addr == nil {
		return "", fmt.Errorf("line %s has no known address (no keepalive yet)", line.ID)
	}
	code = sanitizeProto(code)
	id := s.nextID()
	ch := make(chan string, 64)
	s.sessions.Store(id, ch)
	defer s.sessions.Delete(id)

	addr := line.Addr
	send := func(m string) { s.sendTo(addr, m) }
	cmd := fmt.Sprintf("USSD %s %s %s", id, line.Password, code) // no trailing newline (leaks into the code otherwise)
	s.dbg("USSD line=%s code=%s", line.ID, code)
	send(cmd)

	overall := time.NewTimer(time.Duration(s.cfg.USSDTimeout) * time.Second)
	defer overall.Stop()
	retx := time.NewTimer(time.Duration(s.cfg.USSDRetransmit) * time.Second)
	defer retx.Stop()
	for {
		select {
		case <-overall.C:
			send(fmt.Sprintf("DONE %s\n", id))
			s.dbg("USSD line=%s -> timeout", line.ID)
			return "", fmt.Errorf("ussd timeout")
		case <-retx.C:
			send(cmd) // re-send only after a long interval (default 60s), or it breaks the USSD session
			retx.Reset(time.Duration(s.cfg.USSDRetransmit) * time.Second)
		case resp := <-ch:
			tok := strings.Fields(resp)
			if len(tok) == 0 {
				continue
			}
			switch tok[0] {
			case "USSD":
				reply := ""
				if parts := strings.SplitN(resp, " ", 3); len(parts) >= 3 {
					reply = parts[2]
				}
				send(fmt.Sprintf("DONE %s\n", id))
				s.dbg("USSD line=%s -> %q", line.ID, reply)
				return reply, nil
			case "USSDERROR":
				send(fmt.Sprintf("DONE %s\n", id))
				s.dbg("USSD line=%s -> error %q", line.ID, resp)
				return "", fmt.Errorf("ussd error: %s", resp)
			case "USSDEXIT":
				send(fmt.Sprintf("DONE %s\n", id))
				s.dbg("USSD line=%s -> exit", line.ID)
				return "", nil
			}
		}
	}
}

// ----------------------------------------------------------------------------
// MySQL: connection + outbox queue worker
// ----------------------------------------------------------------------------

func (s *Server) initDB() error {
	c := s.cfg.DB
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&charset=utf8mb4&loc=Local",
		c.User, c.Password, c.Host, c.Port, c.Name)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxLifetime(3 * time.Minute)
	if err := db.Ping(); err != nil {
		db.Close()
		return err
	}
	s.dbp.Store(db)
	return nil
}

func (s *Server) dbConnectRetry(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		if err := s.initDB(); err != nil {
			s.elog.Printf("db reconnect: %v", err)
			continue
		}
		log.Printf("MySQL connected (after retry)")
		s.reconcileSending()
		go s.outboxLoop(ctx)
		return
	}
}

// reconcileSending requeues rows left in 'sending' by a previous crash/restart
// (only those that never reached 'sent', i.e. sent_at IS NULL).
func (s *Server) reconcileSending() {
	db := s.DB()
	if db == nil {
		return
	}
	res, err := db.Exec("UPDATE " + s.outbox() + " SET status='queued' WHERE status='sending' AND sent_at IS NULL")
	if err != nil {
		s.elog.Printf("reconcile sending: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("reconciled %d stuck 'sending' rows -> queued", n)
	}
}

func (s *Server) outboxLoop(ctx context.Context) {
	sem := make(chan struct{}, 8) // max concurrent sends across lines
	t := time.NewTicker(time.Duration(s.cfg.DB.PollSec) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		db := s.DB()
		if db == nil {
			continue
		}
		ot := s.outbox()
		rows, err := db.Query("SELECT id, line, to_number, text FROM " + ot +
			" WHERE status='queued' ORDER BY id LIMIT 100")
		if err != nil {
			s.elog.Printf("outbox query: %v", err)
			continue
		}
		type job struct {
			id       int64
			line     sql.NullString
			to, text string
		}
		var jobs []job
		for rows.Next() {
			var j job
			if err := rows.Scan(&j.id, &j.line, &j.to, &j.text); err != nil {
				s.elog.Printf("outbox scan: %v", err)
				continue
			}
			jobs = append(jobs, j)
		}
		if err := rows.Err(); err != nil {
			s.elog.Printf("outbox rows: %v", err)
		}
		rows.Close()
		for _, j := range jobs {
			res, err := db.Exec("UPDATE "+ot+" SET status='sending' WHERE id=? AND status='queued'", j.id)
			if err != nil {
				s.elog.Printf("outbox claim: %v", err)
				continue
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			sem <- struct{}{}
			go func(id int64, lineID, to, text string) {
				defer func() {
					if r := recover(); r != nil {
						s.elog.Printf("processSend panic id=%d: %v", id, r)
						s.execRetry("UPDATE "+s.outbox()+" SET status='queued' WHERE id=? AND status='sending'", id)
					}
					<-sem
				}()
				s.processSend(id, lineID, to, text)
			}(j.id, j.line.String, j.to, j.text)
		}
	}
}

func (s *Server) processSend(id int64, lineID, to, text string) {
	ot := s.outbox()
	ln := s.pickLine(lineID)
	if ln == nil {
		s.execRetry("UPDATE "+ot+" SET status='queued' WHERE id=?", id) // no alive line — retry next poll
		return
	}
	ok, smsNo, detail := s.sendSMS(ln, to, text)
	if detail == "shutting_down" {
		// Never attempted (shutdown in progress). Leave the row 'sending' with sent_at NULL so the
		// next start's reconcileSending puts it back to 'queued' — no duplicate, no loss.
		return
	}
	if ok {
		var smsNoVal interface{}
		if n, err := strconv.Atoi(smsNo); err == nil {
			smsNoVal = n
		}
		s.execRetry("UPDATE "+ot+" SET status='sent', sms_no=?, line=?, error_code=NULL, sent_at=NOW() WHERE id=?",
			smsNoVal, ln.ID, id)
	} else {
		s.execRetry("UPDATE "+ot+" SET status='failed', error_code=?, line=?, sent_at=NOW() WHERE id=?",
			detail, ln.ID, id)
	}
}

// execRetry runs a write up to 3 times; the message is already sent at this point, so we
// must not lose the status — retrying beats leaving the row stuck.
func (s *Server) execRetry(query string, args ...interface{}) {
	for i := 0; i < 3; i++ {
		db := s.DB()
		if db == nil {
			break
		}
		if _, err := db.Exec(query, args...); err == nil {
			return
		} else {
			s.elog.Printf("db exec (try %d): %v", i+1, err)
		}
		time.Sleep(time.Second)
	}
	s.elog.Printf("CRIT: db write failed after retries (saved to fallback): %s", query)
	s.appendFallback(map[string]any{"kind": "db_write", "query": query, "args": fmt.Sprintf("%v", args), "ts": time.Now().Format(time.RFC3339)})
}

// ----------------------------------------------------------------------------
// HTTP API
// ----------------------------------------------------------------------------

func (s *Server) httpLoop(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.method("GET", s.hHealth)) // unauthenticated; localhost-only listener
	mux.HandleFunc("/lines", s.auth(s.method("GET", s.hLines)))
	mux.HandleFunc("/sms", s.auth(s.method("POST", s.hSMS)))
	mux.HandleFunc("/ussd", s.auth(s.method("POST", s.hUSSD)))
	mux.HandleFunc("/inbox", s.auth(s.method("GET", s.hInbox)))
	s.srv = &http.Server{Addr: s.cfg.ListenHTTP, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.SendTimeout+5)*time.Second)
		defer cancel()
		s.srv.Shutdown(sc)
	}()
	log.Printf("HTTP API on %s", s.cfg.ListenHTTP)
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http: %v", err)
	}
}

func (s *Server) method(m string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != m {
			w.Header().Set("Allow", m)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		h(w, r)
	}
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.HTTPToken != "" {
			want := "Bearer " + s.cfg.HTTPToken
			got := r.Header.Get("Authorization")
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) hLines(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	out := make([]*Line, 0, len(s.lines))
	for _, ln := range s.lines {
		cp := *ln
		cp.Alive = s.lineAlive(ln) // report usable (LOGIN + recent keepalive), not just last GSM status
		out = append(out, &cp)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, out)
}

func (s *Server) hSMS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Line string `json:"line"`
		To   string `json:"to"`
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	if req.To == "" || req.Text == "" {
		writeJSON(w, 400, map[string]string{"error": "need to + text"})
		return
	}
	if !validNumber(strings.TrimSpace(req.To)) {
		writeJSON(w, 400, map[string]string{"error": "bad number"})
		return
	}
	ln := s.pickLine(req.Line)
	if ln == nil {
		writeJSON(w, 404, map[string]string{"error": "no alive line"})
		return
	}
	ok, smsNo, detail := s.sendSMS(ln, req.To, req.Text)
	resp := map[string]any{"line": ln.ID}
	if ok {
		resp["status"] = "sent"
		resp["sms_no"] = smsNo
	} else {
		resp["status"] = "failed"
		resp["error"] = detail
	}
	writeJSON(w, 200, resp) // HTTP 200 always; check "status" in the body
}

func (s *Server) hUSSD(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Line string `json:"line"`
		Code string `json:"code"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	if req.Code == "" {
		writeJSON(w, 400, map[string]string{"error": "need code"})
		return
	}
	ln := s.pickLine(req.Line)
	if ln == nil {
		writeJSON(w, 404, map[string]string{"error": "no alive line"})
		return
	}
	reply, err := s.sendUSSD(ln, req.Code)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error(), "line": ln.ID})
		return
	}
	writeJSON(w, 200, map[string]string{"reply": reply, "line": ln.ID})
}

func (s *Server) hInbox(w http.ResponseWriter, r *http.Request) {
	s.inboxMu.Lock()
	out := make([]Inbound, len(s.inbox))
	copy(out, s.inbox)
	s.inboxMu.Unlock()
	writeJSON(w, 200, out)
}

func (s *Server) hHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	total, alive := len(s.lines), 0
	for _, ln := range s.lines {
		if s.lineAlive(ln) {
			alive++
		}
	}
	s.mu.RUnlock()
	writeJSON(w, 200, map[string]any{"ok": true, "lines": total, "alive": alive, "db": s.DB() != nil})
}

// pickLine returns a SNAPSHOT COPY of the named line (if alive), or, when id=="",
// of the alive line with the lowest id (deterministic). The copy avoids a data race
// with handleKeepalive mutating the live struct.
func (s *Server) pickLine(id string) *Line {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var src *Line
	if id != "" {
		if ln := s.lines[id]; ln != nil && s.lineAlive(ln) {
			src = ln
		}
	} else {
		ids := make([]string, 0, len(s.lines))
		for k, ln := range s.lines {
			if s.lineAlive(ln) {
				ids = append(ids, k)
			}
		}
		if len(ids) > 0 {
			sort.Strings(ids)
			src = s.lines[ids[0]]
		}
	}
	if src == nil {
		return nil
	}
	cp := *src
	if src.Addr != nil { // decouple the address from later keepalive pointer swaps
		a := *src.Addr
		cp.Addr = &a
	}
	return &cp
}

// ----------------------------------------------------------------------------
// Webhook + helpers + main
// ----------------------------------------------------------------------------

func (s *Server) fireWebhook(payload map[string]any) {
	select {
	case s.webhookSem <- struct{}{}:
	default:
		s.elog.Printf("webhook backpressure, dropping event")
		return
	}
	go func() {
		defer func() { <-s.webhookSem }()
		b, _ := json.Marshal(payload)
		req, err := http.NewRequest("POST", s.cfg.WebhookURL, bytes.NewReader(b))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if s.cfg.WebhookToken != "" {
			req.Header.Set("Authorization", "Bearer "+s.cfg.WebhookToken)
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			s.elog.Printf("webhook error: %v", err)
			return
		}
		resp.Body.Close()
	}()
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

// isLoopbackAddr reports whether a listen address is bound to loopback only.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false // ":8080" binds every interface
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func first(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// sanitizeProto strips CR/LF so a value can't inject extra protocol lines into a UDP command.
// SMS *text* is deliberately NOT passed through this: newlines are valid in message bodies and
// MSG is length-prefixed inside a single datagram, so an embedded newline can't split a command.
func sanitizeProto(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// appendFallback durably records, as one JSON line next to the config, a DB operation that
// exhausted its retries — so inbound SMS and send statuses are never silently lost when MySQL
// is unreachable. Append-only and for manual recovery; intentionally NOT auto-replayed.
func (s *Server) appendFallback(rec map[string]any) {
	s.fbMu.Lock()
	defer s.fbMu.Unlock()
	if s.fbPath == "" {
		return
	}
	f, err := os.OpenFile(s.fbPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // holds phone numbers + SMS text
	if err != nil {
		s.elog.Printf("fallback open %s: %v", s.fbPath, err)
		return
	}
	defer f.Close()
	b, _ := json.Marshal(rec)
	f.Write(append(b, '\n'))
}

func setupLogging(s *Server, cfgPath string) {
	dir := filepath.Dir(cfgPath)
	s.fbPath = filepath.Join(dir, "goip-bridge.fallback.jsonl")
	max := int64(s.cfg.LogMaxMB) * 1024 * 1024
	mainCW := newCappedWriter(filepath.Join(dir, "goip-bridge.log"), max)
	errCW := newCappedWriter(filepath.Join(dir, "goip-bridge.err.log"), max)
	log.SetOutput(io.MultiWriter(os.Stderr, mainCW))
	s.elog = log.New(io.MultiWriter(os.Stderr, mainCW, errCW), "", log.LstdFlags)
	log.Printf("%s v%s — %s. %s", appName, appVersion, appTagline, appCopyright) // first echo: what version started
	log.Printf("logging to %s (goip-bridge.log + .err.log, cap %d MB, debug=%v)", dir, s.cfg.LogMaxMB, s.cfg.Debug)
}

func main() {
	cfgPath := flag.String("config", "config.json", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("%s v%s — %s. %s\n", appName, appVersion, appTagline, appCopyright)
		return
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	udpAddr, err := net.ResolveUDPAddr("udp", cfg.ListenUDP)
	if err != nil {
		log.Fatalf("resolve %s: %v", cfg.ListenUDP, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("listen udp %s: %v", cfg.ListenUDP, err)
	}

	s := newServer(cfg)
	s.conn = conn
	setupLogging(s, *cfgPath)

	if cfg.HTTPToken == "" && !isLoopbackAddr(cfg.ListenHTTP) {
		log.Printf("WARNING: http_token is empty and the API listens on %s (non-loopback) — the send API is OPEN to the network", cfg.ListenHTTP)
	}
	if len(cfg.allowNets) > 0 {
		log.Printf("device packets restricted to allow_src %v", cfg.AllowSrc)
	}

	if cfg.DB != nil {
		if err := s.initDB(); err != nil {
			s.elog.Printf("WARNING: MySQL connect failed, retrying in background: %v", err)
			go s.dbConnectRetry(ctx)
		} else {
			log.Printf("MySQL connected: %s@%s:%d/%s (inbox=%s outbox=%s)",
				cfg.DB.User, cfg.DB.Host, cfg.DB.Port, cfg.DB.Name, cfg.DB.InboxTable, cfg.DB.OutboxTable)
			s.reconcileSending()
			go s.outboxLoop(ctx)
		}
	}

	log.Printf("goip-bridge listening on UDP %s (GoIP lines register here)", cfg.ListenUDP)
	go s.httpLoop(ctx)
	go s.udpLoop(ctx)

	<-ctx.Done()
	log.Printf("shutting down...")
	// Stop registering new sends, then drain in-flight ones so their 'sent'/'failed' status reaches
	// the DB before the socket closes — otherwise a row stuck in 'sending' is requeued and re-sent.
	// Flipping draining under the lock guarantees no inflight.Add can start after Wait begins.
	s.drainMu.Lock()
	s.draining = true
	s.drainMu.Unlock()
	drained := make(chan struct{})
	go func() { s.inflight.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(time.Duration(cfg.SendTimeout+5) * time.Second):
		s.elog.Printf("shutdown: gave up waiting for in-flight sends")
	}
	conn.Close()
	if db := s.DB(); db != nil {
		db.Close()
	}
}
