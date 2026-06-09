// goip-bridge — standalone GoIP SMS/USSD gateway.
//
// One file, Go + a MySQL driver. Speaks the GoIP "SMS server" UDP protocol
// directly with the hardware and integrates with MySQL: inbound SMS are written
// to an inbox table, outbound SMS are read from an outbox queue and their status
// (sent / failed / delivered) is written back. A small HTTP API (USSD, direct
// send, lines, inbox) and an optional webhook are also provided.
//
// The protocol was reverse-engineered from the original goipcron binary, the
// official SMS-server spec, and live packet captures.
//
// Build:  go build -o goip-bridge .
// Run:    ./goip-bridge -config config.json
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

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
	DB             *DBConfig         `json:"db"`             // optional MySQL integration ("" / absent = off)
	LinePasswords  map[string]string `json:"line_passwords"` // optional override; else learned from keepalive
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
	}
	if c.LinePasswords == nil {
		c.LinePasswords = map[string]string{}
	}
	return c, nil
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
	db   *sql.DB

	mu    sync.RWMutex
	lines map[string]*Line

	sessions sync.Map // sessionID(string) -> chan string

	seq uint64 // monotonic counter for session/ref ids

	inboxMu sync.Mutex
	inbox   []Inbound // ring buffer of recent inbound messages
}

const inboxCap = 500

func newServer(cfg *Config) *Server {
	return &Server{
		cfg:   cfg,
		lines: map[string]*Line{},
		seq:   uint64(time.Now().Unix()),
	}
}

func (s *Server) nextID() string { return strconv.FormatUint(atomic.AddUint64(&s.seq, 1), 10) }

// sendTo writes a raw protocol line to a device address.
func (s *Server) sendTo(addr *net.UDPAddr, msg string) {
	if addr == nil {
		return
	}
	if _, err := s.conn.WriteToUDP([]byte(msg), addr); err != nil {
		log.Printf("udp write to %s failed: %v", addr, err)
	}
}

// ----------------------------------------------------------------------------
// UDP receive loop + dispatch
// ----------------------------------------------------------------------------

func (s *Server) udpLoop() {
	buf := make([]byte, 8192)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("udp read error: %v", err)
			continue
		}
		s.dispatch(string(buf[:n]), addr)
	}
}

func (s *Server) dispatch(payload string, addr *net.UDPAddr) {
	p := strings.TrimRight(payload, "\r\n")
	switch {
	case strings.HasPrefix(p, "req:"):
		s.handleKeepalive(p, addr)
	case isColonEvent(p):
		verb, seq, _ := splitColonEvent(p)
		switch verb {
		case "RECEIVE":
			s.handleReceive(p, seq, addr)
		case "DELIVER":
			s.handleDeliver(p, seq, addr)
		default:
			// CELLINFO / BCCH / CGATT / ... — just acknowledge.
			s.sendTo(addr, fmt.Sprintf("%s %s OK", verb, seq))
		}
	default:
		// Space-delimited response for an in-flight send/USSD session:
		// "PASSWORD <id>", "SEND <id>", "WAIT <id> <ref>", "OK <id> <ref> <sms_no>",
		// "ERROR <id> ...", "USSD <id> <text>", "USSDERROR ...", "USSDEXIT ...".
		tok := strings.Fields(p)
		if len(tok) >= 2 {
			if ch, ok := s.sessions.Load(tok[1]); ok {
				select {
				case ch.(chan string) <- p:
				default:
				}
			}
		}
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
	// req:<n>;id:Go1;pass:Go1;num:..;signal:..;gsm_status:LOGIN;...;imei:..;pro:<carrier>;...
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
	// Long/multipart messages are reassembled by the device into one packet.
	_, _, rest := splitColonEvent(p)
	f, body := fields(rest)
	from := first(f, "srcnum", "num", "src", "sender")
	line := f["id"]
	s.sendTo(addr, fmt.Sprintf("RECEIVE %s OK", seq)) // ack first so the device stops retransmitting

	in := Inbound{Line: line, From: from, Text: body, Time: time.Now()}
	s.storeInbox(in)
	log.Printf("RECV line=%s from=%s len=%d", line, from, len(body))

	if s.db != nil {
		go func() {
			_, err := s.db.Exec("INSERT INTO "+s.cfg.DB.InboxTable+
				" (line, from_number, text, received_at) VALUES (?,?,?,NOW())", line, from, body)
			if err != nil {
				log.Printf("inbox insert: %v", err)
			}
		}()
	}
	if s.cfg.WebhookURL != "" {
		go s.webhook(map[string]any{"type": "sms", "line": line, "from": from, "text": body, "time": in.Time})
	}
}

func (s *Server) handleDeliver(p, seq string, addr *net.UDPAddr) {
	// DELIVER:<n>;id:<line>;password:<pwd>;sms_no:<k>;state:<s>   (state 0 = delivered)
	_, _, rest := splitColonEvent(p)
	f, _ := fields(rest)
	s.sendTo(addr, fmt.Sprintf("DELIVER %s OK", seq))
	line, smsNo, state := f["id"], f["sms_no"], f["state"]
	log.Printf("DLR line=%s sms_no=%s state=%s", line, smsNo, state)

	if s.db != nil && smsNo != "" {
		go func() {
			ot := s.cfg.DB.OutboxTable
			// A DLR can arrive in the same instant the send's 'sent'+sms_no row is committed.
			// Retry until the matching 'sent' row is there (or give up).
			for i := 0; i < 6; i++ {
				var res sql.Result
				var err error
				if state == "0" {
					res, err = s.db.Exec("UPDATE "+ot+" SET status='delivered', delivered_at=NOW()"+
						" WHERE line=? AND sms_no=? AND status='sent' ORDER BY id DESC LIMIT 1", line, smsNo)
				} else {
					res, err = s.db.Exec("UPDATE "+ot+" SET status='failed', error_code=?, delivered_at=NOW()"+
						" WHERE line=? AND sms_no=? AND status='sent' ORDER BY id DESC LIMIT 1", "dlr_state:"+state, line, smsNo)
				}
				if err == nil {
					if n, _ := res.RowsAffected(); n > 0 {
						return
					}
				}
				time.Sleep(1500 * time.Millisecond)
			}
		}()
	}
	if s.cfg.WebhookURL != "" {
		go s.webhook(map[string]any{"type": "dlr", "line": line, "sms_no": smsNo, "state": state, "time": time.Now()})
	}
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

// sendSMS drives one send and returns (ok, sms_no, detail).
//   ok=true:  sent; sms_no is the device counter.
//   ok=false: detail is the failure reason ("errorstatus:N", "PASSWORD", "timeout", ...).
//
// Protocol: MSG -> dev:PASSWORD -> PASSWORD pass -> dev:SEND -> SEND ref num ->
//           dev:WAIT -> dev:OK <id> <ref> <sms_no> (sent) | ERROR <id> <ref> errorstatus:<n>.
// Never re-send SEND while WAITing — the device performs a fresh send per SEND (duplicates).
func (s *Server) sendSMS(line *Line, number, text string) (bool, string, string) {
	if line.Addr == nil {
		return false, "", "no_address"
	}
	id := s.nextID()
	ref := s.nextID()
	ch := make(chan string, 32)
	s.sessions.Store(id, ch)
	defer s.sessions.Delete(id)

	addr := line.Addr
	send := func(m string) { s.sendTo(addr, m) }

	send(fmt.Sprintf("MSG %s %d %s\n", id, len(text), text))
	submitted := false
	overall := time.After(time.Duration(s.cfg.SendTimeout) * time.Second)
	for {
		select {
		case <-overall:
			send(fmt.Sprintf("DONE %s\n", id))
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
				return true, smsNo, ""
			case "ERROR":
				send(fmt.Sprintf("DONE %s\n", id))
				detail := "error"
				if p := strings.SplitN(resp, " ", 3); len(p) >= 3 {
					detail = p[2] // "<ref> errorstatus:<n>" or "PASSWORD"
				}
				return false, "", detail
			}
		}
	}
}

// ----------------------------------------------------------------------------
// Outbound: USSD  ("USSD <id> <pass> <code>" -> "USSD <id> <reply>"); no trailing newline.
// ----------------------------------------------------------------------------

func (s *Server) sendUSSD(line *Line, code string) (string, error) {
	if line.Addr == nil {
		return "", fmt.Errorf("line %s has no known address (no keepalive yet)", line.ID)
	}
	id := s.nextID()
	ch := make(chan string, 32)
	s.sessions.Store(id, ch)
	defer s.sessions.Delete(id)

	addr := line.Addr
	send := func(m string) { s.sendTo(addr, m) }
	cmd := fmt.Sprintf("USSD %s %s %s", id, line.Password, code) // no trailing newline (leaks into the code otherwise)
	send(cmd)

	overall := time.After(time.Duration(s.cfg.USSDTimeout) * time.Second)
	retx := time.Duration(s.cfg.USSDRetransmit) * time.Second
	for {
		select {
		case <-overall:
			send(fmt.Sprintf("DONE %s\n", id))
			return "", fmt.Errorf("ussd timeout")
		case <-time.After(retx):
			send(cmd) // re-send only after a long interval (default 60s), or it breaks the USSD session
		case resp := <-ch:
			tok := strings.Fields(resp)
			if len(tok) == 0 {
				continue
			}
			switch tok[0] {
			case "USSD":
				reply := ""
				if p := strings.SplitN(resp, " ", 3); len(p) >= 3 {
					reply = p[2]
				}
				send(fmt.Sprintf("DONE %s\n", id))
				return reply, nil
			case "USSDERROR":
				send(fmt.Sprintf("DONE %s\n", id))
				return "", fmt.Errorf("ussd error: %s", resp)
			case "USSDEXIT":
				send(fmt.Sprintf("DONE %s\n", id))
				return "", nil
			}
		}
	}
}

// ----------------------------------------------------------------------------
// MySQL: outbox queue worker
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
		return err
	}
	s.db = db
	return nil
}

func (s *Server) outboxLoop() {
	sem := make(chan struct{}, 8) // max concurrent sends across lines
	t := time.NewTicker(time.Duration(s.cfg.DB.PollSec) * time.Second)
	defer t.Stop()
	ot := s.cfg.DB.OutboxTable
	for range t.C {
		rows, err := s.db.Query("SELECT id, line, to_number, text FROM " + ot +
			" WHERE status='queued' ORDER BY id LIMIT 100")
		if err != nil {
			log.Printf("outbox query: %v", err)
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
			if err := rows.Scan(&j.id, &j.line, &j.to, &j.text); err == nil {
				jobs = append(jobs, j)
			}
		}
		rows.Close()
		for _, j := range jobs {
			// claim atomically so a job is sent exactly once
			res, err := s.db.Exec("UPDATE "+ot+" SET status='sending' WHERE id=? AND status='queued'", j.id)
			if err != nil {
				log.Printf("outbox claim: %v", err)
				continue
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			sem <- struct{}{}
			go func(id int64, lineID, to, text string) {
				defer func() { <-sem }()
				s.processSend(id, lineID, to, text)
			}(j.id, j.line.String, j.to, j.text)
		}
	}
}

func (s *Server) processSend(id int64, lineID, to, text string) {
	ot := s.cfg.DB.OutboxTable
	ln := s.pickLine(lineID)
	if ln == nil {
		// no alive line right now — put it back to retry on the next poll
		s.db.Exec("UPDATE "+ot+" SET status='queued' WHERE id=?", id)
		return
	}
	ok, smsNo, detail := s.sendSMS(ln, to, text)
	if ok {
		var smsNoVal interface{}
		if n, err := strconv.Atoi(smsNo); err == nil {
			smsNoVal = n
		}
		s.db.Exec("UPDATE "+ot+" SET status='sent', sms_no=?, line=?, error_code=NULL, sent_at=NOW() WHERE id=?",
			smsNoVal, ln.ID, id)
	} else {
		s.db.Exec("UPDATE "+ot+" SET status='failed', error_code=?, line=?, sent_at=NOW() WHERE id=?",
			detail, ln.ID, id)
	}
}

// ----------------------------------------------------------------------------
// HTTP API
// ----------------------------------------------------------------------------

func (s *Server) httpLoop() {
	mux := http.NewServeMux()
	mux.HandleFunc("/lines", s.auth(s.hLines))
	mux.HandleFunc("/sms", s.auth(s.hSMS))
	mux.HandleFunc("/ussd", s.auth(s.hUSSD))
	mux.HandleFunc("/inbox", s.auth(s.hInbox))
	log.Printf("HTTP API on %s", s.cfg.ListenHTTP)
	if err := http.ListenAndServe(s.cfg.ListenHTTP, mux); err != nil {
		log.Fatalf("http: %v", err)
	}
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.HTTPToken != "" && r.Header.Get("Authorization") != "Bearer "+s.cfg.HTTPToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
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
		out = append(out, &cp)
	}
	s.mu.RUnlock()
	writeJSON(w, 200, out)
}

func (s *Server) hSMS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Line string `json:"line"`
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	if req.To == "" || req.Text == "" {
		writeJSON(w, 400, map[string]string{"error": "need to + text"})
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
	writeJSON(w, 200, resp)
}

func (s *Server) hUSSD(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Line string `json:"line"`
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
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

// pickLine returns the named line (if alive) or, when id=="", the first alive line.
func (s *Server) pickLine(id string) *Line {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id != "" {
		if ln := s.lines[id]; ln != nil && ln.Alive {
			return ln
		}
		return nil
	}
	for _, ln := range s.lines {
		if ln.Alive {
			return ln
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// Webhook + helpers + main
// ----------------------------------------------------------------------------

func (s *Server) webhook(payload map[string]any) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", s.cfg.WebhookURL, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.WebhookToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.WebhookToken)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		log.Printf("webhook error: %v", err)
		return
	}
	resp.Body.Close()
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func first(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

func main() {
	cfgPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", cfg.ListenUDP)
	if err != nil {
		log.Fatalf("resolve %s: %v", cfg.ListenUDP, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("listen udp %s: %v", cfg.ListenUDP, err)
	}
	defer conn.Close()

	s := newServer(cfg)
	s.conn = conn

	if cfg.DB != nil {
		if err := s.initDB(); err != nil {
			log.Printf("WARNING: MySQL disabled — connect failed: %v", err)
		} else {
			log.Printf("MySQL connected: %s@%s:%d/%s (inbox=%s outbox=%s)",
				cfg.DB.User, cfg.DB.Host, cfg.DB.Port, cfg.DB.Name, cfg.DB.InboxTable, cfg.DB.OutboxTable)
			go s.outboxLoop()
		}
	}

	log.Printf("goip-bridge listening on UDP %s (GoIP lines register here)", cfg.ListenUDP)
	go s.httpLoop()
	s.udpLoop()
}
