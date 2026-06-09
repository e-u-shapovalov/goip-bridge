// goip-bridge — standalone GoIP SMS/USSD gateway.
//
// One file, Go stdlib only. Speaks the GoIP "SMS server" UDP protocol directly
// with the hardware (the GoIP box keepalives in, we reply and drive sends/USSD),
// and exposes a small HTTP API + an optional outbound webhook. Replaces the
// goipcron + MySQL + Apache + PHP stack.
//
// Protocol was reverse-engineered from the original goipcron binary and a live
// packet capture. Device does GSM7/UCS-2 encoding and multipart split/reassembly,
// so we send/receive whole message text.
//
// Build:  go build -o goip-bridge .
// Run:    ./goip-bridge -config config.json
package main

import (
	"bytes"
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
)

// ----------------------------------------------------------------------------
// Config
// ----------------------------------------------------------------------------

type Config struct {
	ListenUDP     string            `json:"listen_udp"`      // device-facing, e.g. ":44444"
	ListenHTTP    string            `json:"listen_http"`     // API, e.g. "127.0.0.1:8080"
	HTTPToken     string            `json:"http_token"`      // Bearer token for the API ("" = open)
	WebhookURL    string            `json:"webhook_url"`     // POST inbound SMS + DLR here ("" = off)
	WebhookToken  string            `json:"webhook_token"`   // sent as Bearer to the webhook
	SendTimeout   int               `json:"send_timeout_sec"`
	USSDTimeout   int               `json:"ussd_timeout_sec"`
	RetransmitSec int               `json:"retransmit_sec"`
	LinePasswords map[string]string `json:"line_passwords"` // optional override; else learned from keepalive
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
		c.USSDTimeout = 60
	}
	if c.RetransmitSec == 0 {
		c.RetransmitSec = 5
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
		payload := string(buf[:n])
		s.dispatch(payload, addr)
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
			// CELLINFO / BCCH / CGATT / remain_time / ... — just acknowledge.
			s.sendTo(addr, fmt.Sprintf("%s %s OK", verb, seq))
		}
	default:
		// Space-delimited response for an in-flight send/USSD session:
		// "PASSWORD <id>", "SEND <id>", "WAIT <id> <ref>", "ERROR <id> <ref> ...",
		// "USSD <id> <text>", "USSDERROR <id> ...", "USSDEXIT <id>".
		tok := strings.Fields(p)
		if len(tok) >= 2 {
			if ch, ok := s.sessions.Load(tok[1]); ok {
				select {
				case ch.(chan string) <- p:
				default:
				}
				return
			}
		}
		// Unknown / stray packet — ignore quietly.
	}
}

// isColonEvent reports whether p looks like "VERB:<seq>;...".
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

func splitColonEvent(p string) (verb, seq string, rest string) {
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

// fields parses a ";"-separated key:value list, but treats everything after the
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
	// req:<n>;id:Go1;pass:Go1;num:..;signal:..;gsm_status:LOGIN;...;imei:..;imsi:..;iccid:..;pro:<carrier>;...
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
	// CONFIRMED live:
	//   RECEIVE:<n>;id:<line>;password:<pwd>;srcnum:<from>;msg:<text>
	//   e.g. RECEIVE:2;id:Go1;password:Go1;srcnum:+996557622222;msg:Test 111
	// Multipart long SMS arrives already reassembled by the device in one packet.
	_, _, rest := splitColonEvent(p)
	f, body := fields(rest)
	from := first(f, "srcnum", "num", "src", "sender")
	line := f["id"]
	s.sendTo(addr, fmt.Sprintf("RECEIVE %s OK", seq)) // ack first so device stops retransmitting

	in := Inbound{Line: line, From: from, Text: body, Time: time.Now()}
	s.storeInbox(in)
	log.Printf("RECV line=%s from=%s len=%d", line, from, len(body))
	if s.cfg.WebhookURL != "" {
		go s.webhook(map[string]any{"type": "sms", "line": line, "from": from, "text": body, "time": in.Time})
	}
}

func (s *Server) handleDeliver(p, seq string, addr *net.UDPAddr) {
	// DELIVER:<n>;id:<line>;password:<pwd>;sms_no:<k>;state:<s>
	_, _, rest := splitColonEvent(p)
	f, _ := fields(rest)
	s.sendTo(addr, fmt.Sprintf("DELIVER %s OK", seq))
	log.Printf("DLR line=%s sms_no=%s state=%s", f["id"], f["sms_no"], f["state"])
	if s.cfg.WebhookURL != "" {
		go s.webhook(map[string]any{"type": "dlr", "line": f["id"], "sms_no": f["sms_no"], "state": f["state"], "time": time.Now()})
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
// Outbound: SMS send  (MSG -> PASSWORD -> SEND handshake, confirmed live)
// ----------------------------------------------------------------------------

func (s *Server) sendSMS(line *Line, number, text string) (string, error) {
	if line.Addr == nil {
		return "", fmt.Errorf("line %s has no known address (no keepalive yet)", line.ID)
	}
	id := s.nextID()
	ref := s.nextID()
	ch := make(chan string, 32)
	s.sessions.Store(id, ch)
	defer s.sessions.Delete(id)

	addr := line.Addr
	pass := line.Password
	send := func(m string) { s.sendTo(addr, m) }

	send(fmt.Sprintf("MSG %s %d %s\n", id, len(text), text))
	state := "MSG"
	overall := time.After(time.Duration(s.cfg.SendTimeout) * time.Second)
	retx := time.Duration(s.cfg.RetransmitSec) * time.Second

	for {
		select {
		case <-overall:
			send(fmt.Sprintf("DONE %s\n", id))
			return "timeout", nil
		case <-time.After(retx):
			// UDP is lossy — re-drive the current step.
			switch state {
			case "MSG":
				send(fmt.Sprintf("MSG %s %d %s\n", id, len(text), text))
			case "SENT":
				send(fmt.Sprintf("SEND %s %s %s\n", id, ref, number))
			}
		case resp := <-ch:
			tok := strings.Fields(resp)
			switch tok[0] {
			case "PASSWORD":
				send(fmt.Sprintf("PASSWORD %s %s\n", id, pass))
				state = "PASSWORD"
			case "SEND":
				if state == "SENT" {
					// bare "SEND <id>" after we provided the recipient = accepted by network.
					send(fmt.Sprintf("DONE %s\n", id))
					return "sent", nil
				}
				// device is ready for a recipient
				send(fmt.Sprintf("SEND %s %s %s\n", id, ref, number))
				state = "SENT"
			case "WAIT":
				// still processing; keep waiting
			case "ERROR":
				code := ""
				if len(tok) >= 3 {
					code = tok[len(tok)-1]
				}
				send(fmt.Sprintf("DONE %s\n", id))
				return "failed " + code, nil
			}
		}
	}
}

// ----------------------------------------------------------------------------
// Outbound: USSD  (CONFIRMED live: "USSD <id> <pass> <code>" -> "USSD <id> <reply>")
// Note: USSD command is sent WITHOUT a trailing newline, else "\n" leaks into the code.
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
	send(fmt.Sprintf("USSD %s %s %s\n", id, line.Password, code))

	overall := time.After(time.Duration(s.cfg.USSDTimeout) * time.Second)
	retx := time.Duration(s.cfg.RetransmitSec) * time.Second
	for {
		select {
		case <-overall:
			send(fmt.Sprintf("DONE %s\n", id))
			return "", fmt.Errorf("ussd timeout")
		case <-time.After(retx):
			send(fmt.Sprintf("USSD %s %s %s", id, line.Password, code))
		case resp := <-ch:
			tok := strings.Fields(resp)
			switch tok[0] {
			case "USSD":
				// "USSD <id> <reply text...>"
				reply := ""
				if len(tok) >= 3 {
					reply = strings.SplitN(resp, " ", 3)[2]
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
		if s.cfg.HTTPToken != "" {
			if r.Header.Get("Authorization") != "Bearer "+s.cfg.HTTPToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
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

func (s *Server) lineByID(id string) *Line {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lines[id]
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
		Line   string `json:"line"`
		To     string `json:"to"`
		Text   string `json:"text"`
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
	status, err := s.sendSMS(ln, req.To, req.Text)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error(), "line": ln.ID})
		return
	}
	writeJSON(w, 200, map[string]string{"status": status, "line": ln.ID})
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
// Webhook
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
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("webhook error: %v", err)
		return
	}
	resp.Body.Close()
}

// ----------------------------------------------------------------------------
// helpers + main
// ----------------------------------------------------------------------------

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
	log.Printf("goip-bridge listening on UDP %s (GoIP lines register here)", cfg.ListenUDP)

	go s.httpLoop()
	s.udpLoop()
}
