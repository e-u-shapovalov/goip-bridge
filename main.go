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
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Build/identity metadata. appVersion can be overridden at build time:
//
//	go build -ldflags "-X main.appVersion=0.5.0" .
const (
	appName      = "goip-bridge"
	appTagline   = "GoIP SMS/USSD gateway"
	appRepoURL   = "https://github.com/e-u-shapovalov/goip-bridge"
	latestAPIURL = "https://api.github.com/repos/e-u-shapovalov/goip-bridge/releases/latest"
	latestAsset  = "https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/"
	appCopyright = "(c) Evgenii Shapovalov 2026"
)

var appVersion = "0.5.0"

// printBanner writes the boxed identity header shown by -version, at startup, and next to the
// first-run config prompt.
func printBanner(w io.Writer) {
	printBox(w, []string{
		fmt.Sprintf("%s v%s", appName, appVersion),
		appTagline,
		appCopyright,
		appRepoURL,
	})
}

func printBox(w io.Writer, lines []string) {
	width := 0
	for _, line := range lines {
		if len(line) > width {
			width = len(line)
		}
	}
	border := "+" + strings.Repeat("-", width+2) + "+"
	fmt.Fprintln(w, border)
	for _, line := range lines {
		fmt.Fprintf(w, "| %-*s |\n", width, line)
	}
	fmt.Fprintln(w, border)
}

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

// PacingRange is the pause window (seconds) between consecutive sends on ONE line.
// MinSec==MaxSec = fixed pause; MaxSec<=0 = no pause; otherwise a random value in [Min,Max].
type PacingRange struct {
	MinSec int `json:"min_sec"`
	MaxSec int `json:"max_sec"`
}

// SendPacing throttles outbound SMS per line: a SIM sends one SMS at a time, so each
// line sends serially and then waits a (random) delay before its next send. Default
// applies to every line; PerLine overrides by line id. Absent in config = {3,10}.
type SendPacing struct {
	Default PacingRange            `json:"default"`
	PerLine map[string]PacingRange `json:"per_line"`
}

// WebhookRetry controls reliable webhook delivery: events are held in RAM and retried
// with exponential backoff (BaseSec, then doubling) for up to MaxHours. Absent = {3,5}.
type WebhookRetry struct {
	MaxHours int `json:"max_hours"`
	BaseSec  int `json:"base_sec"`
}

type Config struct {
	ListenUDP      string            `json:"listen_udp"`     // device-facing, e.g. "172.16.172.3:44444"
	ListenHTTP     string            `json:"listen_http"`    // API, e.g. "127.0.0.1:8080"
	HTTPToken      string            `json:"http_token"`     // Bearer token for the API ("" = open)
	WebhookURL     string            `json:"webhook_url"`    // optional POST of inbound SMS, DLR and queue result events ("" = off)
	WebhookToken   string            `json:"webhook_token"`  // sent as Bearer to the webhook
	FailThreshold  int               `json:"fail_threshold"` // consecutive send failures that emit line_failing (default 10)
	CheckUpdates   bool              `json:"check_updates"`  // optional startup check for a newer GitHub release (default false)
	SendTimeout    int               `json:"send_timeout_sec"`
	USSDTimeout    int               `json:"ussd_timeout_sec"`
	USSDRetransmit int               `json:"ussd_retransmit_sec"`
	Debug          bool              `json:"debug"`               // detailed SMS/USSD/inbound logging to file
	DebugLine      bool              `json:"debug_line"`          // per-line raw keepalive log (incl. password) to goip-bridge.line-<id>.log, each capped at 3 MB
	LogMaxMB       int               `json:"log_max_mb"`          // per-file log cap in MB (default 10)
	ClearLogsStart *bool             `json:"clear_logs_on_start"` // archive current bridge logs to .prev on startup (default true)
	LineDeadSec    int               `json:"line_dead_after_sec"` // line treated as dead after this many seconds without keepalive (default 120)
	AllowSrc       []string          `json:"allow_src"`           // optional CIDR/IP allow-list for device UDP packets (empty = accept all; firewall is then the only barrier)
	DB             *DBConfig         `json:"db"`                  // optional MySQL integration (absent = off)
	LinePasswords  map[string]string `json:"line_passwords"`      // per-line password: presented when sending AND (if set) required on inbound packets; unset = learned from keepalive
	SendPacing     *SendPacing       `json:"send_pacing"`         // per-line outbound throttle (absent = random 3-10s between sends on a line)
	DefaultLines   []string          `json:"default_lines"`       // round-robin set for outbox rows with line=NULL/'' (empty = all alive lines)
	WebhookRetry   *WebhookRetry     `json:"webhook_retry"`       // reliable webhook delivery (absent = up to 3h, base 5s backoff)

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
	if err := json.Unmarshal(stripJSONComments(b), c); err != nil {
		return nil, err
	}
	if c.ListenUDP == "" {
		c.ListenUDP = ":44444"
	}
	if c.ListenHTTP == "" {
		c.ListenHTTP = "127.0.0.1:8080"
	}
	// Use <= 0 (not == 0) so a negative misconfig falls back to the default instead of reaching
	// time.NewTicker/NewTimer (a non-positive duration panics the ticker / busy-loops the timer).
	if c.SendTimeout <= 0 {
		c.SendTimeout = 45
	}
	if c.USSDTimeout <= 0 {
		c.USSDTimeout = 120
	}
	if c.USSDRetransmit <= 0 {
		c.USSDRetransmit = 60
	}
	if c.LogMaxMB <= 0 {
		c.LogMaxMB = 10
	}
	if c.LineDeadSec <= 0 {
		c.LineDeadSec = 120
	}
	if c.FailThreshold <= 0 {
		c.FailThreshold = defaultFailThreshold
	}
	if c.ClearLogsStart == nil {
		v := true
		c.ClearLogsStart = &v
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
		if c.DB.PollSec <= 0 {
			c.DB.PollSec = 3
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
	if c.SendPacing == nil {
		c.SendPacing = &SendPacing{Default: PacingRange{MinSec: 3, MaxSec: 10}}
	}
	if c.SendPacing.PerLine == nil {
		c.SendPacing.PerLine = map[string]PacingRange{}
	}
	if c.WebhookRetry == nil {
		c.WebhookRetry = &WebhookRetry{}
	}
	if c.WebhookRetry.MaxHours <= 0 {
		c.WebhookRetry.MaxHours = 3
	}
	if c.WebhookRetry.BaseSec <= 0 {
		c.WebhookRetry.BaseSec = 5
	}
	nets, err := parseAllowSrc(c.AllowSrc)
	if err != nil {
		return nil, err
	}
	c.allowNets = nets
	return c, nil
}

// stripJSONComments lets the config be JSONC: it removes // line comments and
// /* */ block comments (ignoring those inside strings, so "http://..." is safe)
// and drops trailing commas before } or ]. On comment-free strict JSON it is a
// no-op, so existing configs keep working unchanged.
func stripJSONComments(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	pendingComma := -1 // index in out of a comma awaiting a verdict, or -1
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '/' { // line comment
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n') // preserve line numbers in parse errors
			}
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '*' { // block comment
			out = append(out, ' ') // leave a token separator so "1/*x*/2" can't silently become "12"
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				if b[i] == '\n' {
					out = append(out, '\n') // preserve line numbers in parse errors
				}
				i++
			}
			i++ // land on the '/'; outer loop's i++ steps past it
			continue
		}
		switch c {
		case '"':
			pendingComma = -1
			inStr = true
			out = append(out, c)
		case ' ', '\t', '\r', '\n':
			out = append(out, c)
		case ',':
			out = append(out, c)
			pendingComma = len(out) - 1
		case '}', ']':
			if pendingComma >= 0 { // it was a trailing comma — drop it
				out = append(out[:pendingComma], out[pendingComma+1:]...)
				pendingComma = -1
			}
			out = append(out, c)
		default:
			pendingComma = -1
			out = append(out, c)
		}
	}
	return out
}

// stdinIsTTY reports whether stdin is an interactive terminal (so we may prompt).
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// promptConfigLang asks interactively which language the generated config should
// use and waits for ru/en. Returns "" on EOF / no terminal.
func promptConfigLang() string {
	fmt.Fprint(os.Stderr, "На каком языке создать конфиг? / Config language? [ru/en]: ")
	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "ru", "rus", "russian", "ру", "рус":
			return "ru"
		case "en", "eng", "english", "ен", "англ":
			return "en"
		}
		if err != nil {
			return ""
		}
		fmt.Fprint(os.Stderr, "Введите ru или en / type ru or en: ")
	}
}

// writeDefaultConfig creates a fully-annotated JSONC config (lang = "ru" or "en").
// It refuses to overwrite an existing file.
func writeDefaultConfig(path, lang string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%q already exists — refusing to overwrite", path)
	}
	body := configTemplateEN
	if lang == "ru" {
		body = configTemplateRU
	}
	return os.WriteFile(path, []byte(body), 0o600) // config holds tokens/passwords
}

// afterCreateMsg is printed once a fresh config is written, telling the user the
// minimum they must fill in before the daemon can do anything useful.
func afterCreateMsg(lang, path string) string {
	if lang == "ru" {
		return fmt.Sprintf(`Конфиг создан: %s

Теперь откройте его и заполните минимально необходимое для старта:
  • http_token   — длинный случайный токен для HTTP API (иначе API открыт всем)
  • listen_udp   — порт должен совпадать с "SMS Server Port" в каждом GoIP

И выберите способ интеграции (одно из двух):
  • webhook_url  — получать входящие SMS, DLR и результаты очереди по HTTP-вебхуку, ИЛИ
  • раскомментируйте блок "db" — если используете очередь MySQL (outbox/inbox)

Потом запустите снова:  ./%s -config %s
`, path, appName, path)
	}
	return fmt.Sprintf(`Config created: %s

Now open it and fill in the minimum required to start:
  • http_token   — a long random token for the HTTP API (otherwise the API is open)
  • listen_udp   — the port must match "SMS Server Port" in every GoIP

And pick one integration mode:
  • webhook_url  — receive inbound SMS, DLR and queue results via HTTP webhook, OR
  • uncomment the "db" block — if you use the MySQL queue (outbox/inbox)

Then run again:  ./%s -config %s
`, path, appName, path)
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
		// Clear the handle: after a failed rotation the old *os.File is already Closed, and without
		// this the Write guard (cw.f == nil) wouldn't catch it — every later write would go to a dead
		// fd (silently dropped) and re-enter rotation. nil makes those writes no-op until the disk frees.
		cw.f = nil
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
		if cw.f == nil { // reopen failed (disk full / no perms) — drop this write instead of nil-deref panic
			return len(p), nil
		}
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
	cfg       *Config
	conn      *net.UDPConn
	dbp       atomic.Pointer[sql.DB] // nil until/unless MySQL is connected
	srv       *http.Server
	startedAt time.Time   // process start, for the status command's uptime
	elog      *log.Logger // errors -> goip-bridge.err.log (+ main + stderr)
	fmain     *log.Logger // file-only main log (no stderr) for lines that get colored stderr separately
	ferr      *log.Logger // file-only main+err log (no stderr), same purpose for WARN lines

	mu    sync.RWMutex
	lines map[string]*Line

	sessions sync.Map // sessionID(string) -> chan string

	seq uint64 // monotonic counter for session/ref ids

	inboxMu sync.Mutex
	inbox   []Inbound // ring buffer of recent inbound messages

	seenMu    sync.Mutex
	seen      map[string]time.Time // dedup of inbound RECEIVE/DELIVER (key = "verb:line:seq")
	seenPurge time.Time            // last time old dedup keys were evicted (time-based, not size-based)

	webhookSem chan struct{}
	httpClient *http.Client

	inflight sync.WaitGroup // in-flight sends, drained on shutdown so status reaches the DB
	bgWrites atomic.Int64   // background DB-write goroutines (insertInbox/applyDLR/stat) in flight; drained at shutdown
	drainMu  sync.RWMutex   // guards `draining`; held (R) across inflight.Add so Add never races Wait
	draining bool           // set true at shutdown; blocks new sends from registering
	fbMu     sync.Mutex
	fbPath   string // goip-bridge.fallback.jsonl — durable record of DB writes that exhausted retries

	logDir    string                   // dir holding the logs; per-line debug logs (debug_line) live here too
	lineLogMu sync.Mutex               // guards lineLogs
	lineLogs  map[string]*cappedWriter // debug_line: line id -> its own raw keepalive log (lazy)

	paceMu       sync.Mutex           // guards lineBusy/lineNextSend/rrIdx/lineSends
	lineBusy     map[string]bool      // a send is in flight on this line (one SMS at a time per SIM)
	lineNextSend map[string]time.Time // earliest moment this line may send again (pacing)
	rrIdx        int                  // round-robin cursor for outbox rows with line=NULL
	lineSends    map[string][]sendRec // per-line ring of recent send outcomes (channel health)
	lineFailing  map[string]bool      // line_failing already emitted until the next success

	whMu             sync.Mutex           // guards whQueue/queuedAnnounced
	whQueue          []*webhookEvent      // reliable webhook delivery queue (in RAM, retried with backoff)
	queuedAnnounced  map[string]time.Time // in-process dedup for queued webhook events by guid
	lineStateMu      sync.Mutex
	lineAlivePrev    map[string]bool // previous lineAlive snapshot for line_down/line_up events
	coloredStatusTTY bool            // stderr is a TTY; status lines may use ANSI there only
}

// sendRec is one recent send outcome for a line (channel-health ring).
type sendRec struct {
	ok bool
	at time.Time
}

// webhookEvent is one pending webhook delivery held in RAM until a 2xx (or max_hours).
type webhookEvent struct {
	payload  map[string]any
	body     []byte
	attempt  int
	inflight bool
	firstAt  time.Time
	nextAt   time.Time
}

const inboxCap = 500

// maxSMSTextBytes caps the SMS body so it can't overflow a UDP datagram (~65507 B). An oversized
// text would make WriteToUDP fail and leave sendSMS waiting out send_timeout_sec, blocking the line.
// 4096 bytes is far above any real SMS (even long multipart) yet well under the datagram limit.
const maxSMSTextBytes = 4096

// maxUSSDCodeLen caps the USSD code at the outbox to_number column width (VARCHAR(64)); a longer
// value would be silently truncated by MySQL (or rejected in strict mode) and sent as a wrong code.
const maxUSSDCodeLen = 64

func newServer(cfg *Config) *Server {
	return &Server{
		cfg:          cfg,
		startedAt:    time.Now(),
		lines:        map[string]*Line{},
		seen:         map[string]time.Time{},
		seq:          uint64(time.Now().Unix()),
		elog:         log.New(os.Stderr, "", log.LstdFlags), // replaced in main once log files are open
		webhookSem:   make(chan struct{}, 16),
		lineBusy:     map[string]bool{},
		lineNextSend: map[string]time.Time{},
		lineSends:    map[string][]sendRec{},
		lineFailing:  map[string]bool{},
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		queuedAnnounced: map[string]time.Time{},
		lineAlivePrev:   map[string]bool{},
	}
}

func (s *Server) DB() *sql.DB    { return s.dbp.Load() }
func (s *Server) nextID() string { return strconv.FormatUint(atomic.AddUint64(&s.seq, 1), 10) }
func (s *Server) outbox() string { return s.cfg.DB.OutboxTable }

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

// goDBWrite runs a best-effort background DB write (inbox insert / DLR update / stat row) while
// counting it in bgWrites, so shutdown can wait for it before db.Close() instead of killing the
// goroutine mid-write and losing the row (the counter is a plain atomic, so unlike a WaitGroup it
// can't panic if a new write starts during the drain — see drainBackgroundWrites in main).
func (s *Server) goDBWrite(f func()) {
	s.bgWrites.Add(1)
	go func() {
		defer s.bgWrites.Add(-1)
		f()
	}()
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
	return s.lineAliveAt(ln, time.Now())
}

func (s *Server) lineAliveAt(ln *Line, now time.Time) bool {
	return ln.Alive && now.Sub(ln.LastSeen) < time.Duration(s.cfg.LineDeadSec)*time.Second
}

// lineCount returns how many lines have ever registered (an upper bound on sends per poll).
func (s *Server) lineCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.lines)
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

const seenWindow = 10 * time.Minute // dedup retention for retransmitted RECEIVE/DELIVER packets

// seenRecently reports whether key was already recorded (and records it now). Old keys are purged
// on a time basis (at most once a minute), so the dedup window stays ~10 minutes regardless of
// traffic. The previous size-only purge (>8192 entries) let keys live indefinitely at low traffic,
// which could falsely drop a genuinely new RECEIVE/DELIVER after a device reboot reused a seq, and
// let the map grow without bound during a high-traffic burst.
func (s *Server) seenRecently(key string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	now := time.Now()
	if now.Sub(s.seenPurge) > time.Minute {
		for k, t := range s.seen {
			if now.Sub(t) > seenWindow {
				delete(s.seen, k)
			}
		}
		s.seenPurge = now
	}
	// Age-aware: a key older than the window is NOT a dup even if the purge hasn't run yet (it runs at
	// most once a minute, so a stale key can linger ~1 min). Without this, a rebooted device that reuses
	// a seq in that gap would have its genuinely-new RECEIVE/DELIVER wrongly dropped.
	t, ok := s.seen[key]
	dup := ok && now.Sub(t) <= seenWindow
	s.seen[key] = now
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
	// Split on the ";msg:" field boundary (or a leading "msg:"), not the first "msg:" substring —
	// otherwise a value of an earlier field that contains "msg:" would be mistaken for the body start.
	if strings.HasPrefix(s, "msg:") {
		body = s[len("msg:"):]
		s = ""
	} else if idx := strings.Index(s, ";msg:"); idx >= 0 {
		body = s[idx+len(";msg:"):]
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

// linePassOK authenticates an inbound packet's password for a line. If the line
// is pinned in line_passwords, the packet's password must match (constant-time);
// if it is NOT pinned, any password is accepted (the learn-from-keepalive default).
func (s *Server) linePassOK(id, got string) bool {
	want, pinned := s.cfg.LinePasswords[id]
	if !pinned {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) handleKeepalive(p string, addr *net.UDPAddr) {
	_, seq, rest := splitColonEvent(p)
	f, _ := fields(rest)
	id := f["id"]
	if id == "" || len(id) > 64 { // real GoIP client ids are short and fit the DB line VARCHAR(64)
		return
	}
	if s.cfg.DebugLine {
		// One file per line with the full raw keepalive (password, signal, IMEI/IMSI/ICCID, carrier...).
		fmt.Fprintf(s.lineLog(id), "%s from=%s %s\n", time.Now().Format("2006-01-02 15:04:05.000"), addr, p)
	}
	if !s.linePassOK(id, first(f, "pass", "password")) {
		// Pinned line + wrong password: don't register, don't even ack (reg).
		s.dbg("rejected keepalive line=%s from %s: bad password", id, addr)
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
	if strings.TrimSpace(line) == "" { // can't route/store/dedup without a line id — drop, no ack
		s.dbg("bad RECEIVE from %s: missing id", addr)
		return
	}
	if len(line) > 64 || len(seq) > 32 { // bound untrusted id/seq: real ids fit VARCHAR(64), seq is a short int
		s.dbg("bad RECEIVE from %s: oversized id/seq", addr)
		return
	}
	if !s.linePassOK(line, f["password"]) { // pinned line + wrong password: drop, no ack, no store
		s.dbg("rejected RECEIVE line=%s from %s: bad password", line, addr)
		return
	}
	s.sendTo(addr, fmt.Sprintf("RECEIVE %s OK", seq)) // ack first so the device stops retransmitting
	if s.cfg.Debug {                                  // flag inbound arriving from an IP that doesn't match the line's keepalive source
		s.mu.RLock()
		ln := s.lines[line]
		var regIP net.IP // copy under the lock; handleKeepalive may swap ln.Addr concurrently
		if ln != nil && ln.Addr != nil {
			regIP = ln.Addr.IP
		}
		s.mu.RUnlock()
		if regIP != nil && !regIP.Equal(addr.IP) {
			s.dbg("RX line=%s from unexpected ip %s (registered %s)", line, addr.IP, regIP)
		}
	}

	// Drop retransmits (device repeats RECEIVE:<n> if our ack was lost) — else we'd double-store.
	if s.seenRecently("R:" + line + ":" + seq) {
		s.dbg("RX dup ignored line=%s seq=%s", line, seq)
		return
	}

	in := Inbound{Line: line, From: from, Text: body, Time: time.Now()}
	s.storeInbox(in)
	log.Printf("RECV line=%q from=%q len=%d", line, from, len(body)) // %q: from/line are untrusted (alphanumeric sender id) — avoid log injection
	s.dbg("RX line=%s from=%s text=%q", line, from, body)

	// Gate on cfg.DB (configured), not DB() (connected): if MySQL is mid-reconnect, insertInbox sees
	// db==nil and records the SMS to the durable fallback journal instead of dropping it silently.
	if s.cfg.DB != nil {
		s.goDBWrite(func() { s.insertInbox(line, from, body) }) // tracked so shutdown drains it before db.Close()
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
	line, smsNo, state := f["id"], f["sms_no"], f["state"]
	if len(line) > 64 || len(seq) > 32 { // bound untrusted id/seq (see handleReceive)
		s.dbg("bad DELIVER from %s: oversized id/seq", addr)
		return
	}
	if !s.linePassOK(line, f["password"]) { // pinned line + wrong password: drop, no ack
		s.dbg("rejected DELIVER line=%s from %s: bad password", line, addr)
		return
	}
	s.sendTo(addr, fmt.Sprintf("DELIVER %s OK", seq))
	log.Printf("DLR line=%q sms_no=%q state=%q", line, smsNo, state) // %q: fields come straight from the packet

	if s.seenRecently("D:" + line + ":" + seq) {
		return
	}
	// Only match a numeric sms_no against the BIGINT column (a junk value would be coerced by MySQL
	// and could touch the wrong row). Gate on cfg.DB so a DLR during reconnect reaches the fallback.
	if s.cfg.DB != nil {
		if n, err := strconv.ParseInt(smsNo, 10, 64); err == nil && n > 0 {
			s.goDBWrite(func() { s.applyDLR(line, smsNo, state) }) // tracked so shutdown drains it before db.Close()
		} else if smsNo != "" {
			s.dbg("DLR line=%s ignored non-numeric sms_no=%q", line, smsNo)
		}
	}
	if s.cfg.WebhookURL != "" {
		ev := map[string]any{"type": "dlr", "line": line, "sms_no": smsNo, "state": state, "time": time.Now()}
		if desc := describeError("dlr_state:" + state); desc != "" {
			ev["state_desc"] = desc // 0 -> "delivered (received by SME)", others -> failure cause
		}
		s.fireWebhook(ev)
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
				dbCode, _ := annotateError("dlr_state:" + state)
				res, err = db.Exec("UPDATE "+s.outbox()+" SET status='failed', error_code=?, delivered_at=NOW()"+base, dbCode, line, smsNo)
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
//
//	dev:WAIT -> dev:OK <id> <ref> <sms_no> (sent) | ERROR <id> <ref> errorstatus:<n>.
//
// Never re-send SEND while WAITing — the device performs a fresh send per SEND (duplicates).
func (s *Server) sendSMS(line *Line, number, text string) (bool, string, string) {
	// The caller registers/drains the in-flight send (beginSend / inflight.Done) so the whole
	// operation — including the status write it performs afterwards — is covered on shutdown.
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
	pass := sanitizeProto(line.Password) // strip CR/LF so a learned password can't inject a protocol line
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
				send(fmt.Sprintf("PASSWORD %s %s\n", id, pass))
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
	// In-flight registration/drain is owned by the caller (see sendSMS).
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
	cmd := fmt.Sprintf("USSD %s %s %s", id, sanitizeProto(line.Password), code) // no trailing newline (leaks into the code otherwise)
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
// Error code reference (GoIP errorstatus / DLR state -> human description)
//
// INFERRED from the GSM standards (07.05 / 03.40 / 04.11) and the Quectel M35 AT
// command set, NOT read from the device firmware (we have no access to it). Best-
// effort hints: accurate for the standard code space, but a given module/firmware
// may diverge. See AGENTS.md "Железо / модуль GoIP8".
// ----------------------------------------------------------------------------

// cmsErrorText maps a +CMS ERROR code (GoIP "errorstatus:<n>" on a failed SEND) to its standard
// meaning. 1..255 are GSM 03.40/04.11 transfer causes; 300..511 are GSM 07.05 device (ME/SIM) errors.
var cmsErrorText = map[int]string{
	1: "Unassigned (unallocated) number", 8: "Operator determined barring", 10: "Call barred",
	17: "Network failure", 21: "Short message transfer rejected",
	22: "Congestion / memory capacity exceeded", 27: "Destination out of service",
	28: "Unidentified subscriber", 29: "Facility rejected", 30: "Unknown subscriber",
	38: "Network out of order", 41: "Temporary failure", 42: "Congestion",
	47: "Resources unavailable, unspecified", 50: "Requested facility not subscribed",
	69: "Requested facility not implemented", 81: "Invalid short message transfer reference value",
	95: "Invalid message, unspecified", 96: "Invalid mandatory information",
	97:  "Message type non-existent or not implemented",
	98:  "Message not compatible with short message protocol state",
	99:  "Information element non-existent or not implemented",
	111: "Protocol error, unspecified", 127: "Interworking, unspecified",
	128: "Telematic interworking not supported", 129: "Short message Type 0 not supported",
	130: "Cannot replace short message", 143: "Unspecified TP-PID error",
	144: "Data coding scheme (alphabet) not supported", 145: "Message class not supported",
	159: "Unspecified TP-DCS error", 160: "Command cannot be actioned", 161: "Command unsupported",
	175: "Unspecified TP-Command error", 176: "TPDU not supported", 192: "SC busy",
	193: "No SC subscription", 194: "SC system failure", 195: "Invalid SME address",
	196: "Destination SME barred", 197: "SM rejected — duplicate SM", 198: "TP-VPF not supported",
	199: "TP-VP not supported", 208: "SIM SMS storage full", 209: "No SMS storage capability in SIM",
	210: "Error in MS", 211: "Memory capacity exceeded", 212: "SIM Application Toolkit busy",
	255: "Unspecified error cause",
	300: "ME failure", 301: "SMS service of ME reserved", 302: "Operation not allowed",
	303: "Operation not supported", 304: "Invalid PDU mode parameter", 305: "Invalid text mode parameter",
	310: "SIM not inserted", 311: "SIM PIN required", 312: "PH-SIM PIN required", 313: "SIM failure",
	314: "SIM busy", 315: "SIM wrong", 316: "SIM PUK required", 317: "SIM PIN2 required",
	318: "SIM PUK2 required", 320: "Memory failure", 321: "Invalid memory index", 322: "Memory full",
	330: "SMSC address unknown", 331: "No network service", 332: "Network timeout",
	340: "No +CNMA acknowledgement expected", 500: "Unknown error (often weak signal / no balance)",
}

// tpStatusText maps a GSM 03.40 TP-Status (the GoIP DLR "state" / "dlr_state:<n>") to its meaning.
// This is a DIFFERENT code space from CMS errors above: 0 = delivered, the rest are SC outcomes.
var tpStatusText = map[int]string{
	0: "delivered (received by SME)", 1: "forwarded by SC, delivery not confirmed", 2: "replaced by SC",
	32: "congestion (temporary, still trying)", 33: "SME busy (temporary, still trying)",
	34: "no response from SME (temporary, still trying)", 35: "service rejected (temporary, still trying)",
	36: "QoS not available (temporary, still trying)", 37: "error in SME (temporary, still trying)",
	64: "remote procedure error (permanent)", 65: "incompatible destination (permanent)",
	66: "connection rejected by SME (permanent)", 67: "not obtainable (permanent)",
	68: "QoS not available (permanent)", 69: "no interworking available (permanent)",
	70: "SM validity period expired (permanent)", 71: "SM deleted by originating SME (permanent)",
	72: "SM deleted by SC administration (permanent)", 73: "SM does not exist (permanent)",
	96: "congestion (SC gave up)", 97: "SME busy (SC gave up)", 98: "no response from SME (SC gave up)",
	99: "service rejected (SC gave up)", 100: "QoS not available (SC gave up)", 101: "error in SME (SC gave up)",
}

// tpStatusClass classifies a TP-Status by its GSM 03.40 range when the exact value is unmapped.
func tpStatusClass(n int) string {
	switch {
	case n >= 0 && n <= 31:
		return "completed"
	case n >= 32 && n <= 63:
		return "temporary error (SC still trying)"
	case n >= 64 && n <= 95:
		return "permanent error (SC gave up)"
	case n >= 96 && n <= 127:
		return "temporary error (SC gave up)"
	default:
		return ""
	}
}

// parseLeadingInt reads the leading run of digits from s (after trimming spaces); ok=false if none.
func parseLeadingInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:i])
	return n, err == nil
}

// describeError turns a raw GoIP failure detail into a human description, or "" if unknown. It
// recognizes "errorstatus:<n>" (a +CMS ERROR, possibly prefixed by the send ref e.g. "1 errorstatus:38")
// and "dlr_state:<n>" (a GSM 03.40 TP-Status). Other details (timeout, bad_number, ...) return "".
func describeError(detail string) string {
	if i := strings.Index(detail, "errorstatus:"); i >= 0 {
		if n, ok := parseLeadingInt(detail[i+len("errorstatus:"):]); ok {
			if t := cmsErrorText[n]; t != "" {
				return t
			}
			if n >= 512 {
				return "manufacturer-specific error"
			}
			return ""
		}
	}
	if i := strings.Index(detail, "dlr_state:"); i >= 0 {
		if n, ok := parseLeadingInt(detail[i+len("dlr_state:"):]); ok {
			if t := tpStatusText[n]; t != "" {
				return t
			}
			return tpStatusClass(n)
		}
	}
	return ""
}

// annotateError returns the value to store in the DB error_code column (the raw detail with the
// human description appended when known — backward-compatible, it still starts with the raw code)
// and the standalone description for the webhook/HTTP error_desc field ("" when unknown).
func annotateError(detail string) (dbCode, desc string) {
	desc = describeError(detail)
	if desc == "" {
		return detail, ""
	}
	return detail + " — " + desc, desc
}

// ----------------------------------------------------------------------------
// MySQL: connection + outbox queue worker
// ----------------------------------------------------------------------------

func (s *Server) initDB() error {
	c := s.cfg.DB
	// Build the DSN via the driver's config so special characters in the password/db name are
	// escaped (string concatenation breaks on '@' '/' ':' '?' in a password). The I/O timeouts
	// bound every query at the connection level, so a hung MySQL can't block a goroutine forever.
	mc := mysql.NewConfig()
	mc.User = c.User
	mc.Passwd = c.Password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	mc.DBName = c.Name
	mc.ParseTime = true
	mc.Loc = time.Local
	mc.Params = map[string]string{"charset": "utf8mb4"}
	mc.Timeout = 10 * time.Second      // dial timeout
	mc.ReadTimeout = 30 * time.Second  // abort a query that hangs (network split / locked table)
	mc.WriteTimeout = 30 * time.Second // abort a stalled write
	db, err := sql.Open("mysql", mc.FormatDSN())
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

// reconcileSending resolves rows left in 'sending' by a previous crash/restart. The two message
// types are handled differently on purpose:
//
//   - USSD is NOT idempotent — a re-send can repeat a charge or balance transfer, and a row stuck
//     in 'sending' may already have reached the modem. So we never auto-resend it: it is failed and
//     left for manual review.
//   - SMS is re-sendable; a rare duplicate after a hard crash is tolerable and better than a lost
//     message, so a stuck SMS goes back to 'queued'.
//
// A graceful shutdown never leaves a transmitted row here (sends are drained first), so this only
// runs for hard crashes / kills.
func (s *Server) reconcileSending() {
	db := s.DB()
	if db == nil {
		return
	}
	// Leave sent_at NULL: a row stuck in 'sending' was claimed but its sent_at is only written together
	// with the final status, so it was (almost certainly) never transmitted. Stamping NOW() here would
	// fake a "sent today" timestamp for messages that never left.
	// USSD and control commands ('cmd') are NOT idempotent — a USSD resend can re-charge, and a 'reset'
	// cmd cancels whatever is queued at re-run time (not at the original time), so a crash mid-reset must
	// not silently re-run it. Both are failed/interrupted for manual review, never auto-resent.
	if res, err := db.Exec("UPDATE " + s.outbox() + " SET status='failed', error_code='interrupted' WHERE status='sending' AND type IN ('ussd','cmd')"); err != nil {
		s.elog.Printf("reconcile ussd/cmd: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("reconciled %d interrupted USSD/cmd 'sending' -> failed (not auto-resent)", n)
	}
	if res, err := db.Exec("UPDATE " + s.outbox() + " SET status='queued' WHERE status='sending' AND type NOT IN ('ussd','cmd') AND sent_at IS NULL"); err != nil {
		s.elog.Printf("reconcile sms: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("reconciled %d stuck SMS 'sending' -> queued", n)
	}
}

// pacingFor returns the pacing window for a line (per-line override, else default).
func (c *Config) pacingFor(line string) PacingRange {
	if r, ok := c.SendPacing.PerLine[line]; ok {
		return r
	}
	return c.SendPacing.Default
}

// pacingDelay is the pause to wait after a send on this line (0 = no pause).
func (s *Server) pacingDelay(line string) time.Duration {
	r := s.cfg.pacingFor(line)
	if r.MaxSec <= 0 {
		return 0
	}
	min, max := r.MinSec, r.MaxSec
	if min < 0 {
		min = 0
	}
	if max < min {
		max = min
	}
	d := min
	if max > min {
		d = min + rand.Intn(max-min+1)
	}
	return time.Duration(d) * time.Second
}

// aliveCandidates lists lines eligible for round-robin: default_lines (in order)
// filtered to alive, or — if default_lines is empty — all alive lines, sorted.
func (s *Server) aliveCandidates() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var c []string
	if len(s.cfg.DefaultLines) > 0 {
		for _, id := range s.cfg.DefaultLines {
			if ln := s.lines[id]; ln != nil && s.lineAlive(ln) {
				c = append(c, id)
			}
		}
		return c
	}
	for id, ln := range s.lines {
		if s.lineAlive(ln) {
			c = append(c, id)
		}
	}
	sort.Strings(c)
	return c
}

// lineReadyLocked reports whether a line can start a send now (idle + pacing elapsed). Hold paceMu.
func (s *Server) lineReadyLocked(id string) bool {
	return !s.lineBusy[id] && !time.Now().Before(s.lineNextSend[id])
}

// claimLineForSend marks a specific line busy if it is alive and ready; false otherwise.
func (s *Server) claimLineForSend(id string) bool {
	s.mu.RLock()
	ln := s.lines[id]
	alive := ln != nil && s.lineAlive(ln)
	s.mu.RUnlock()
	if !alive {
		return false
	}
	s.paceMu.Lock()
	defer s.paceMu.Unlock()
	if !s.lineReadyLocked(id) {
		return false
	}
	s.lineBusy[id] = true
	return true
}

// pickRoundRobin returns the next ready candidate (round-robin) and marks it busy, or "".
func (s *Server) pickRoundRobin() string {
	cands := s.aliveCandidates()
	if len(cands) == 0 {
		return ""
	}
	s.paceMu.Lock()
	defer s.paceMu.Unlock()
	n := len(cands)
	for k := 0; k < n; k++ {
		id := cands[(s.rrIdx+k)%n]
		if s.lineReadyLocked(id) {
			s.rrIdx = (s.rrIdx + k + 1) % 1000000
			s.lineBusy[id] = true
			return id
		}
	}
	return ""
}

// releaseLine clears the busy flag WITHOUT a pacing penalty (no SMS was transmitted).
func (s *Server) releaseLine(id string) {
	s.paceMu.Lock()
	s.lineBusy[id] = false
	s.paceMu.Unlock()
}

// finishLine clears busy and arms the pacing delay (called after a real send attempt).
func (s *Server) finishLine(id string) {
	s.paceMu.Lock()
	s.lineNextSend[id] = time.Now().Add(s.pacingDelay(id))
	s.lineBusy[id] = false
	s.paceMu.Unlock()
}

// newGUID returns an unguessable public message id: microtime (for rough ordering in logs)
// plus 96 bits of crypto-random. The random part keeps /status/{guid} and /message/{guid}
// non-enumerable even when the API token is weak or absent.
func (s *Server) newGUID() string {
	var b [12]byte
	if _, err := crand.Read(b[:]); err != nil { // crypto source unavailable: fall back to the counter
		return fmt.Sprintf("%d-%s", time.Now().UnixMicro(), s.nextID())
	}
	return fmt.Sprintf("%d-%x", time.Now().UnixMicro(), b[:])
}

const lineSendsCap = 20         // recent outcomes kept per line
const defaultFailThreshold = 10 // consecutive failures that flag a channel as suspect / line_failing

// recordSend appends a send outcome to a line's channel-health ring.
func (s *Server) recordSend(line string, ok bool) {
	if strings.TrimSpace(line) == "" {
		return
	}
	var event map[string]any
	s.paceMu.Lock()
	r := append(s.lineSends[line], sendRec{ok: ok, at: time.Now()})
	if len(r) > lineSendsCap {
		r = r[len(r)-lineSendsCap:]
	}
	s.lineSends[line] = r
	streak := 0
	for i := len(r) - 1; i >= 0; i-- {
		if r[i].ok {
			break
		}
		streak++
	}
	threshold := s.failThreshold()
	if ok {
		if s.lineFailing[line] {
			delete(s.lineFailing, line)
			event = map[string]any{"type": "line_recovered", "line": line, "fail_streak": 0, "threshold": threshold, "time": time.Now()}
		}
	} else if threshold > 0 && streak >= threshold && !s.lineFailing[line] {
		s.lineFailing[line] = true
		event = map[string]any{"type": "line_failing", "line": line, "fail_streak": streak, "threshold": threshold, "time": time.Now()}
	}
	s.paceMu.Unlock()
	if event != nil {
		event["channel"] = s.channelHealth(line)
		s.fireWebhook(event)
	}
}

func (s *Server) failThreshold() int {
	if s == nil || s.cfg == nil || s.cfg.FailThreshold <= 0 {
		return defaultFailThreshold
	}
	return s.cfg.FailThreshold
}

// channelHealth summarizes a line's state for webhook/status payloads: alive, how long
// since the last keepalive, and a recent-failure streak that hints at ban / no balance.
func (s *Server) channelHealth(line string) map[string]any {
	s.mu.RLock()
	ln := s.lines[line]
	alive := ln != nil && s.lineAlive(ln)
	var lastSeenAgo interface{}
	if ln != nil && !ln.LastSeen.IsZero() {
		lastSeenAgo = int(time.Since(ln.LastSeen).Seconds())
	}
	s.mu.RUnlock()
	s.paceMu.Lock()
	recs := s.lineSends[line]
	streak := 0
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].ok {
			break
		}
		streak++
	}
	total := len(recs)
	s.paceMu.Unlock()
	h := map[string]any{
		"line": line, "alive": alive, "last_seen_ago_sec": lastSeenAgo,
		"recent_sends": total, "recent_fail_streak": streak, "suspect": false,
	}
	if streak >= s.failThreshold() {
		h["suspect"] = true
		h["suspect_reason"] = fmt.Sprintf("%d sends in a row failed — possible ban or no balance", streak)
	}
	return h
}

func (s *Server) lineMonitor(ctx context.Context) {
	if s.cfg.WebhookURL == "" {
		return
	}
	interval := time.Duration(s.cfg.LineDeadSec/4) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.checkLineTransitions(now)
		}
	}
}

func (s *Server) checkLineTransitions(now time.Time) {
	type lineState struct {
		id    string
		alive bool
	}
	s.mu.RLock()
	states := make([]lineState, 0, len(s.lines))
	for id, ln := range s.lines {
		states = append(states, lineState{id: id, alive: s.lineAliveAt(ln, now)})
	}
	s.mu.RUnlock()
	if len(states) == 0 {
		return
	}
	sort.Slice(states, func(i, j int) bool { return states[i].id < states[j].id })

	var events []map[string]any
	s.lineStateMu.Lock()
	for _, st := range states {
		prev, known := s.lineAlivePrev[st.id]
		if !known {
			s.lineAlivePrev[st.id] = st.alive
			continue
		}
		if prev == st.alive {
			continue
		}
		s.lineAlivePrev[st.id] = st.alive
		typ := "line_down"
		if st.alive {
			typ = "line_up"
		}
		events = append(events, map[string]any{
			"type":           typ,
			"line":           st.id,
			"dead_after_sec": s.cfg.LineDeadSec,
			"time":           now,
		})
	}
	s.lineStateMu.Unlock()
	for _, ev := range events {
		if line, _ := ev["line"].(string); line != "" {
			ev["channel"] = s.channelHealth(line)
		}
		s.fireWebhook(ev)
	}
}

type outboxJob struct {
	id         int64
	guid, line sql.NullString
	typ, to    string
	text       sql.NullString
}

// ussdRE is the USSD code alphabet: digits and * # + only. Spaces/letters would inject extra tokens
// into the "USSD <id> <pass> <code>" command (the SMS path guards the recipient number the same way).
var ussdRE = regexp.MustCompile(`^[0-9*#+]+$`)

// validateOutboxJob rejects a queued row that can never send cleanly, checked BEFORE a line is claimed
// so one bad row can't occupy a line for send_timeout_sec (an oversized text makes WriteToUDP fail
// silently in sendSMS, which then waits out the full timeout). typ is already normalized to sms/ussd.
func validateOutboxJob(typ, to string, text sql.NullString) (string, bool) {
	switch typ {
	case "sms":
		if !validNumber(strings.TrimSpace(to)) {
			return "bad_number", false
		}
		if !text.Valid || text.String == "" { // don't send a blank SMS (HTTP /sms rejects empty text too)
			return "no_text", false
		}
		if len(text.String) > maxSMSTextBytes {
			return "text_too_long", false
		}
	case "ussd":
		if c := strings.TrimSpace(to); c == "" || len(c) > maxUSSDCodeLen || !ussdRE.MatchString(c) {
			return "bad_ussd_code", false
		}
	}
	return "", true
}

func (s *Server) outboxLoop(ctx context.Context) {
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

		// A SIM sends one message at a time, so a line takes at most ONE send at a time and only
		// after its pacing delay elapses. Rows whose target line is dead/busy/not-yet-due stay
		// 'queued' and are retried next poll. We page through the queue by id rather than reading a
		// single LIMIT 100: otherwise a front-of-queue batch bound to a dead/busy line would block
		// routable rows behind it (head-of-line blocking). Stop once every line already has a send
		// this tick (can't start more than one per line), the queue is drained, or a page cap hits.
		lineCount := s.lineCount()
		notReady := map[string]bool{} // lines confirmed busy/dead this tick (skipped without re-locking)
		rrDead := false               // round-robin found no ready line this tick
		dispatched := 0
		lastID := int64(0)
		for page := 0; page < 20; page++ {
			rows, err := db.Query("SELECT id, guid, line, type, to_number, text FROM "+ot+
				" WHERE status='queued' AND id > ? ORDER BY id LIMIT 100", lastID)
			if err != nil {
				s.elog.Printf("outbox query: %v", err)
				break
			}
			var jobs []outboxJob
			for rows.Next() {
				var j outboxJob
				if err := rows.Scan(&j.id, &j.guid, &j.line, &j.typ, &j.to, &j.text); err != nil {
					s.elog.Printf("outbox scan: %v", err)
					continue
				}
				jobs = append(jobs, j)
			}
			if err := rows.Err(); err != nil {
				s.elog.Printf("outbox rows: %v", err)
			}
			rows.Close()
			if len(jobs) == 0 {
				break
			}
			for _, j := range jobs {
				lastID = j.id

				typ := strings.TrimSpace(j.typ)
				if typ == "" {
					typ = "sms"
				}
				if typ == "cmd" { // control command (status/reset): no line, no pacing — claim, run, move on
					guid := j.guid.String
					if guid == "" {
						guid = s.newGUID()
					}
					res, err := db.Exec("UPDATE "+ot+" SET status='sending', guid=? WHERE id=? AND status='queued'", guid, j.id)
					if err != nil {
						s.elog.Printf("cmd claim: %v", err)
						continue
					}
					if n, _ := res.RowsAffected(); n == 0 {
						continue // already claimed/cancelled elsewhere
					}
					go s.processCommand(j.id, guid, j.to)
					continue
				}
				if typ != "sms" && typ != "ussd" { // unknown type: never send it, fail for review
					s.execRetry("UPDATE "+ot+" SET status='failed', error_code='bad_type' WHERE id=? AND status='queued'", j.id)
					continue
				}
				if code, okJob := validateOutboxJob(typ, j.to, j.text); !okJob { // reject bad rows before claiming a line
					s.execRetry("UPDATE "+ot+" SET status='failed', error_code=? WHERE id=? AND status='queued'", code, j.id)
					continue
				}

				var target string
				if id := strings.TrimSpace(j.line.String); j.line.Valid && id != "" {
					if notReady[id] {
						continue
					}
					if s.claimLineForSend(id) {
						target = id
					} else {
						notReady[id] = true
						continue
					}
				} else {
					if rrDead {
						continue
					}
					if target = s.pickRoundRobin(); target == "" { // marks the chosen line busy
						rrDead = true
						continue
					}
				}

				guid := j.guid.String
				if guid == "" {
					guid = s.newGUID() // app inserted the row without a guid — assign one at claim
				}
				res, err := db.Exec("UPDATE "+ot+" SET status='sending', guid=? WHERE id=? AND status='queued'", guid, j.id)
				if err != nil {
					s.elog.Printf("outbox claim: %v", err)
					s.releaseLine(target)
					continue
				}
				if n, _ := res.RowsAffected(); n == 0 {
					s.releaseLine(target) // another worker / a cancel took the row — no pacing penalty
					continue
				}
				extra := map[string]any{}
				if typ == "ussd" {
					extra["code"] = j.to
				} else {
					extra["to"] = j.to
				}
				s.fireQueued(guid, typ, target, extra)
				go s.processSend(j.id, guid, typ, target, j.to, j.text.String)
				dispatched++
			}
			if len(jobs) < 100 {
				break // queue drained
			}
			if lineCount > 0 && dispatched >= lineCount {
				break // every line already has a send this tick; the rest must wait
			}
		}
	}
}

// processSend runs one outbox row (sms or ussd) through the already-claimed line `lineID`,
// writes the result back, records channel health, and arms the pacing delay. The line was
// marked busy by the caller; `to` holds the recipient (sms) or the USSD code (ussd).
func (s *Server) processSend(id int64, guid, typ, lineID, to, text string) {
	// Own the in-flight registration for the WHOLE row (send + status write + webhook enqueue) so a
	// shutdown drains it fully. If we're already draining, the row was never transmitted: leave it
	// 'sending' (sent_at NULL) for reconcileSending to requeue (SMS) / fail (USSD).
	if !s.beginSend() {
		s.releaseLine(lineID)
		return
	}
	defer s.inflight.Done()
	defer func() {
		if r := recover(); r != nil {
			s.elog.Printf("processSend panic id=%d: %v", id, r)
			// Release the line FIRST: if execRetry below panics too, the line must not stay busy forever.
			s.releaseLine(lineID)
			// We can't know whether the device was already hit — fail rather than requeue, so a
			// send is never silently duplicated. The status guard avoids clobbering a written result.
			s.execRetry("UPDATE "+s.outbox()+" SET status='failed', error_code='panic' WHERE id=? AND status='sending'", id)
		}
	}()
	ot := s.outbox()
	ln := s.pickLine(lineID)
	if ln == nil { // line died between routing and now — requeue, no pacing penalty
		s.execRetry("UPDATE "+ot+" SET status='queued' WHERE id=?", id)
		s.releaseLine(lineID)
		return
	}

	if typ == "ussd" {
		reply, err := s.sendUSSD(ln, to) // to_number holds the USSD code
		if err != nil {
			s.execRetry("UPDATE "+ot+" SET status='failed', error_code=?, line=?, sent_at=NOW() WHERE id=?",
				err.Error(), ln.ID, id)
			s.recordSend(lineID, false)
			s.fireWebhook(map[string]any{"type": "failed", "id": guid, "msg_type": "ussd", "line": ln.ID,
				"error": err.Error(), "channel": s.channelHealth(ln.ID), "time": time.Now()})
		} else {
			s.execRetry("UPDATE "+ot+" SET status='done', reply=?, error_code=NULL, line=?, sent_at=NOW() WHERE id=?",
				reply, ln.ID, id)
			s.recordSend(lineID, true)
			s.fireWebhook(map[string]any{"type": "done", "id": guid, "msg_type": "ussd", "line": ln.ID,
				"reply": reply, "channel": s.channelHealth(ln.ID), "time": time.Now()})
		}
		s.finishLine(lineID)
		return
	}

	ok, smsNo, detail := s.sendSMS(ln, to, text)
	if ok {
		var smsNoVal interface{}
		if n, err := strconv.Atoi(smsNo); err == nil {
			smsNoVal = n
		}
		s.execRetry("UPDATE "+ot+" SET status='sent', sms_no=?, line=?, error_code=NULL, sent_at=NOW() WHERE id=?",
			smsNoVal, ln.ID, id)
		s.recordSend(lineID, true)
		s.fireWebhook(map[string]any{"type": "sent", "id": guid, "msg_type": "sms", "line": ln.ID,
			"sms_no": smsNo, "channel": s.channelHealth(ln.ID), "time": time.Now()})
	} else {
		dbCode, desc := annotateError(detail)
		s.execRetry("UPDATE "+ot+" SET status='failed', error_code=?, line=?, sent_at=NOW() WHERE id=?",
			dbCode, ln.ID, id)
		s.recordSend(lineID, false)
		ev := map[string]any{"type": "failed", "id": guid, "msg_type": "sms", "line": ln.ID,
			"error": detail, "channel": s.channelHealth(ln.ID), "time": time.Now()}
		if desc != "" {
			ev["error_desc"] = desc
		}
		s.fireWebhook(ev)
	}
	s.finishLine(lineID) // arm pacing delay after a real send attempt
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
// Control commands: status (diagnostics) + reset (soft purge)
//
// Available BOTH via the MySQL queue (outbox row type='cmd', to_number='status'|'reset')
// and via HTTP (POST /stats, POST /reset). The reply body is IDENTICAL on both paths and
// for both sinks: it is written as one JSON row into the inbox table (line='system',
// from_number='goip-bridge') AND POSTed to the webhook (when webhook_url is set). The HTTP
// path also returns it inline. So the answer is always "awaited in the inbox", uniformly.
// ----------------------------------------------------------------------------

const statInboxLine = "system"      // goip_inbox.line for a command reply (marks it apart from a real SMS)
const statInboxFrom = "goip-bridge" // goip_inbox.from_number for a command reply

// buildStats gathers a full diagnostic snapshot: version, uptime, Go runtime + memory, system RAM
// (Linux, best-effort via /proc/meminfo), every line's state, and the MySQL queue/inbox counts.
func (s *Server) buildStats() map[string]any {
	now := time.Now()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	s.mu.RLock()
	total := len(s.lines)
	alive := 0
	lineList := make([]map[string]any, 0, total)
	for _, ln := range s.lines {
		a := s.lineAliveAt(ln, now)
		if a {
			alive++
		}
		var ago interface{}
		if !ln.LastSeen.IsZero() {
			ago = int(now.Sub(ln.LastSeen).Seconds())
		}
		lineList = append(lineList, map[string]any{
			"id": ln.ID, "alive": a, "signal": ln.Signal, "gsm_status": ln.GSMStatus,
			"carrier": ln.Carrier, "num": ln.Num, "last_seen_ago_sec": ago,
		})
	}
	s.mu.RUnlock()
	sort.Slice(lineList, func(i, j int) bool { return lineList[i]["id"].(string) < lineList[j]["id"].(string) })

	uptime := now.Sub(s.startedAt)
	payload := map[string]any{
		"type":       "stat",
		"time":       now,
		"version":    appVersion,
		"started_at": s.startedAt,
		"uptime_sec": int(uptime.Seconds()),
		"uptime":     formatUptime(uptime),
		"runtime": map[string]any{
			"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
			"num_cpu": runtime.NumCPU(), "goroutines": runtime.NumGoroutine(),
		},
		"memory": map[string]any{ // process (Go) memory, bytes
			"alloc": m.Alloc, "total_alloc": m.TotalAlloc, "sys": m.Sys,
			"heap_alloc": m.HeapAlloc, "heap_sys": m.HeapSys, "heap_inuse": m.HeapInuse,
			"stack_inuse": m.StackInuse, "num_gc": m.NumGC,
		},
		"lines": map[string]any{"total": total, "alive": alive, "list": lineList},
	}
	if sys := readSystemMem(); sys != nil {
		payload["system_memory"] = sys // kB, from /proc/meminfo (Linux only)
	}
	payload["db_configured"] = s.cfg.DB != nil
	if db := s.DB(); db != nil {
		payload["db_connected"] = true
		if q := s.queueCounts(db); q != nil {
			payload["queue"] = q
		}
	} else {
		payload["db_connected"] = false
	}
	return payload
}

// formatUptime renders a duration like the GoIP web UI uptime (H:MM:SS), prefixing whole days.
func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int64(d.Seconds())
	days := total / 86400
	h := (total % 86400) / 3600
	mn := (total % 3600) / 60
	sec := total % 60
	if days > 0 {
		return fmt.Sprintf("%dd %02d:%02d:%02d", days, h, mn, sec)
	}
	return fmt.Sprintf("%02d:%02d:%02d", h, mn, sec)
}

// readSystemMem returns selected /proc/meminfo values (in kB) on Linux, or nil elsewhere / on error.
func readSystemMem() map[string]any {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil
	}
	want := map[string]string{
		"MemTotal": "mem_total_kb", "MemFree": "mem_free_kb", "MemAvailable": "mem_available_kb",
		"Buffers": "buffers_kb", "Cached": "cached_kb", "SwapTotal": "swap_total_kb", "SwapFree": "swap_free_kb",
	}
	out := map[string]any{}
	for _, line := range strings.Split(string(b), "\n") {
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key, ok := want[line[:i]]
		if !ok {
			continue
		}
		if n, ok := parseLeadingInt(line[i+1:]); ok {
			out[key] = n
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// queueCounts returns the count of outbox rows per status plus the inbox total (best-effort).
func (s *Server) queueCounts(db *sql.DB) map[string]any {
	out := map[string]any{}
	rows, err := db.Query("SELECT status, COUNT(*) FROM " + s.outbox() + " GROUP BY status")
	if err != nil {
		s.elog.Printf("stat queue counts: %v", err)
		return nil
	}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			s.elog.Printf("stat queue scan: %v", err)
			continue
		}
		out[st] = n
	}
	if err := rows.Err(); err != nil { // a mid-iteration read error must not pass off partial counts as complete
		s.elog.Printf("stat queue rows: %v", err)
	}
	rows.Close()
	var inbox int64
	if err := db.QueryRow("SELECT COUNT(*) FROM " + s.cfg.DB.InboxTable).Scan(&inbox); err == nil {
		out["inbox_total"] = inbox
	}
	return out
}

// reset performs a SOFT reset without restarting the service: it cancels every still-queued outbox
// row (the queue DB user has SELECT/INSERT/UPDATE but NO DELETE, so rows are marked 'cancelled' —
// not deleted — keeping history while ensuring they never send) and clears in-RAM "soft" caches
// (inbound dedup, per-line health/suspect rings, pacing timers, queued-webhook dedup). It does NOT
// touch in-flight sends, the pending-webhook retry queue, or the line registry.
func (s *Server) reset() map[string]any {
	res := map[string]any{}
	if db := s.DB(); db != nil {
		if r, err := db.Exec("UPDATE " + s.outbox() + " SET status='cancelled' WHERE status='queued'"); err != nil {
			s.elog.Printf("reset cancel queued: %v", err)
			res["queue_error"] = err.Error()
		} else {
			n, _ := r.RowsAffected()
			res["cancelled_queued"] = n
		}
	}
	s.seenMu.Lock()
	s.seen = map[string]time.Time{}
	s.seenPurge = time.Time{}
	s.seenMu.Unlock()

	s.paceMu.Lock()
	s.lineSends = map[string][]sendRec{}
	s.lineFailing = map[string]bool{}
	s.lineNextSend = map[string]time.Time{}
	s.paceMu.Unlock()

	s.whMu.Lock()
	s.queuedAnnounced = map[string]time.Time{}
	s.whMu.Unlock()

	res["caches_reset"] = []string{"inbound_dedup", "line_health", "pacing", "queued_announced"}
	return res
}

// runCommand executes a control command by name and returns the reply payload (the identical body
// written to the inbox and sent to the webhook) plus whether the command was recognized.
func (s *Server) runCommand(cmd, trigger, guid string) (map[string]any, bool) {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	var payload map[string]any
	switch cmd {
	case "", "status", "stats", "stat", "uptime", "ping", "info":
		payload = s.buildStats()
		payload["command"] = "status"
	case "reset", "purge", "clear", "flush", "soft_reset", "purge_queue", "clear_queue":
		done := s.reset()
		payload = s.buildStats() // report the post-reset snapshot
		payload["command"] = "reset"
		payload["reset"] = done
	default:
		return map[string]any{"type": "stat", "command": cmd, "trigger": trigger,
			"error": "unknown_cmd", "time": time.Now()}, false
	}
	payload["trigger"] = trigger // "db" or "http"
	if guid != "" {
		payload["id"] = guid
	}
	return payload, true
}

// emitStats delivers a command reply identically to both sinks: as a JSON row in the inbox table
// (when a DB is configured) and to the webhook (when webhook_url is set). The same map is marshaled
// for both, so the inbox text and the webhook body are byte-identical.
func (s *Server) emitStats(payload map[string]any) {
	if s.cfg.DB != nil {
		if body, err := json.Marshal(payload); err == nil {
			s.goDBWrite(func() { s.insertInbox(statInboxLine, statInboxFrom, string(body)) }) // tracked for shutdown drain
		} else {
			s.elog.Printf("stat marshal: %v", err)
		}
	}
	s.fireWebhook(payload) // no-op when webhook_url is empty
}

// processCommand handles an outbox row of type='cmd' (the DB path). The reply lands in the inbox +
// webhook; the outbox row is then closed out (done / failed) so its own status reflects the outcome.
func (s *Server) processCommand(id int64, guid, raw string) {
	// No device I/O, but it reads the line registry / runtime stats / DB in a goroutine — cover it like
	// processSend: (a) recover so a panic here can't kill the daemon (an unrecovered goroutine panic is
	// fatal to the whole process), (b) inflight so shutdown drains it. If already draining, leave the row
	// 'sending' for reconcileSending to fail (cmd is never auto-rerun).
	if !s.beginSend() {
		return
	}
	defer s.inflight.Done()
	defer func() {
		if r := recover(); r != nil {
			s.elog.Printf("processCommand panic id=%d: %v", id, r)
			s.execRetry("UPDATE "+s.outbox()+" SET status='failed', error_code='panic' WHERE id=? AND status='sending'", id)
		}
	}()
	payload, ok := s.runCommand(raw, "db", guid)
	if !ok {
		s.execRetry("UPDATE "+s.outbox()+" SET status='failed', error_code=?, sent_at=NOW() WHERE id=?",
			"unknown_cmd:"+strings.TrimSpace(raw), id)
		return
	}
	s.emitStats(payload)
	s.execRetry("UPDATE "+s.outbox()+" SET status='done', error_code=NULL, sent_at=NOW() WHERE id=?", id)
}

// ----------------------------------------------------------------------------
// HTTP API
// ----------------------------------------------------------------------------

func (s *Server) httpLoop(ctx context.Context, ln net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.method("GET", s.hHealth)) // unauthenticated; localhost-only listener
	mux.HandleFunc("/lines", s.auth(s.method("GET", s.hLines)))
	mux.HandleFunc("/sms", s.auth(s.method("POST", s.hSMS)))
	mux.HandleFunc("/ussd", s.auth(s.method("POST", s.hUSSD)))
	mux.HandleFunc("/inbox", s.auth(s.method("GET", s.hInbox)))
	mux.HandleFunc("/status/", s.auth(s.method("GET", s.hStatus)))     // /status/{guid}
	mux.HandleFunc("/message/", s.auth(s.method("DELETE", s.hCancel))) // /message/{guid}
	mux.HandleFunc("/stats", s.auth(s.method("POST", s.hStats)))       // diagnostics (status command)
	mux.HandleFunc("/reset", s.auth(s.method("POST", s.hReset)))       // soft reset (cancel queued + flush caches)
	// WriteTimeout must outlast the longest synchronous handler: in no-DB mode /sms blocks up to
	// send_timeout_sec and /ussd up to ussd_timeout_sec while the device responds. The read-side
	// timeouts (header/body/idle) bound slow clients without cutting off a legitimately slow send.
	writeTO := s.cfg.SendTimeout
	if s.cfg.USSDTimeout > writeTO {
		writeTO = s.cfg.USSDTimeout
	}
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		WriteTimeout:      time.Duration(writeTO+15) * time.Second,
		MaxHeaderBytes:    1 << 14,
	}
	go func() {
		<-ctx.Done()
		// Match the daemon's drain budget (max of send/USSD timeout), not just SendTimeout — otherwise a
		// long synchronous /ussd (up to ussd_timeout_sec) is force-closed mid-flight on shutdown.
		sc, cancel := context.WithTimeout(context.Background(), time.Duration(writeTO+5)*time.Second)
		defer cancel()
		s.srv.Shutdown(sc)
	}()
	log.Printf("HTTP API on %s", s.cfg.ListenHTTP)
	// The listener is already bound in main (so a bind failure is reported there with full cleanup);
	// only an unexpected Serve error remains, which must NOT os.Exit and skip shutdown — just log it.
	if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.elog.Printf("http serve: %v", err)
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
	if len(req.Text) > maxSMSTextBytes {
		writeJSON(w, 400, map[string]string{"error": "text too long"})
		return
	}
	// With a DB configured, /sms is async: queue the row and return immediately. The scheduler
	// sends it with per-channel pacing and reports the result via webhook + the DB row.
	if s.cfg.DB != nil {
		db := s.DB()
		if db == nil { // configured but disconnected right now — don't silently drop to a sync send
			writeJSON(w, 503, map[string]string{"error": "queue temporarily unavailable (db reconnecting)"})
			return
		}
		guid := s.newGUID()
		var lineVal interface{}
		if strings.TrimSpace(req.Line) != "" {
			lineVal = req.Line
		}
		if _, err := db.Exec("INSERT INTO "+s.outbox()+" (guid, type, line, to_number, text, status, created_at) VALUES (?, 'sms', ?, ?, ?, 'queued', NOW())",
			guid, lineVal, req.To, req.Text); err != nil {
			s.elog.Printf("enqueue sms: %v", err)
			writeJSON(w, 500, map[string]string{"error": "enqueue failed"})
			return
		}
		s.fireQueued(guid, "sms", req.Line, map[string]any{"to": req.To})
		writeJSON(w, 202, map[string]any{"status": "accepted", "id": guid, "queued_at": time.Now().UnixMicro()})
		return
	}
	// No DB configured: legacy synchronous direct send (result in the response).
	if !s.beginSend() {
		writeJSON(w, 503, map[string]string{"error": "shutting down"})
		return
	}
	defer s.inflight.Done()
	// Serialize per line as the queue does (a SIM sends one at a time): claim the line so two
	// concurrent direct sends can't interleave SEND sessions on the same channel. releaseLine (not
	// finishLine) keeps the no-DB path pacing-free — only genuine concurrency is rejected with 409.
	target := strings.TrimSpace(req.Line)
	if target == "" {
		if target = s.pickRoundRobin(); target == "" {
			writeJSON(w, 404, map[string]string{"error": "no alive line"})
			return
		}
	} else if !s.claimLineForSend(target) {
		if s.pickLine(target) == nil {
			writeJSON(w, 404, map[string]string{"error": "no alive line"})
		} else {
			writeJSON(w, 409, map[string]string{"error": "line busy"})
		}
		return
	}
	defer s.releaseLine(target)
	ln := s.pickLine(target)
	if ln == nil {
		writeJSON(w, 404, map[string]string{"error": "no alive line"})
		return
	}
	guid := s.newGUID()
	ok, smsNo, detail := s.sendSMS(ln, req.To, req.Text)
	resp := map[string]any{"id": guid, "line": ln.ID}
	if ok {
		s.recordSend(ln.ID, true)
		resp["status"] = "sent"
		resp["sms_no"] = smsNo
		s.fireWebhook(map[string]any{"type": "sent", "id": guid, "msg_type": "sms", "line": ln.ID,
			"sms_no": smsNo, "channel": s.channelHealth(ln.ID), "time": time.Now()})
	} else {
		s.recordSend(ln.ID, false)
		resp["status"] = "failed"
		resp["error"] = detail
		ev := map[string]any{"type": "failed", "id": guid, "msg_type": "sms", "line": ln.ID,
			"error": detail, "channel": s.channelHealth(ln.ID), "time": time.Now()}
		if desc := describeError(detail); desc != "" {
			resp["error_desc"] = desc
			ev["error_desc"] = desc
		}
		s.fireWebhook(ev)
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
	if len(req.Code) > maxUSSDCodeLen {
		writeJSON(w, 400, map[string]string{"error": "code too long"})
		return
	}
	if !ussdRE.MatchString(strings.TrimSpace(req.Code)) { // digits and * # + only — no spaces/letters
		writeJSON(w, 400, map[string]string{"error": "bad code"})
		return
	}
	// With a DB configured, /ussd is async too: queue it; the reply arrives via webhook + reply column.
	if s.cfg.DB != nil {
		db := s.DB()
		if db == nil { // configured but disconnected right now — don't silently drop to a sync send
			writeJSON(w, 503, map[string]string{"error": "queue temporarily unavailable (db reconnecting)"})
			return
		}
		guid := s.newGUID()
		var lineVal interface{}
		if strings.TrimSpace(req.Line) != "" {
			lineVal = req.Line
		}
		if _, err := db.Exec("INSERT INTO "+s.outbox()+" (guid, type, line, to_number, status, created_at) VALUES (?, 'ussd', ?, ?, 'queued', NOW())",
			guid, lineVal, req.Code); err != nil {
			s.elog.Printf("enqueue ussd: %v", err)
			writeJSON(w, 500, map[string]string{"error": "enqueue failed"})
			return
		}
		s.fireQueued(guid, "ussd", req.Line, map[string]any{"code": req.Code})
		writeJSON(w, 202, map[string]any{"status": "accepted", "id": guid, "queued_at": time.Now().UnixMicro()})
		return
	}
	// No DB configured: legacy synchronous USSD (reply in the response).
	if !s.beginSend() {
		writeJSON(w, 503, map[string]string{"error": "shutting down"})
		return
	}
	defer s.inflight.Done()
	// Serialize per line (see hSMS): a USSD session and an SMS send can't share a SIM at once.
	target := strings.TrimSpace(req.Line)
	if target == "" {
		if target = s.pickRoundRobin(); target == "" {
			writeJSON(w, 404, map[string]string{"error": "no alive line"})
			return
		}
	} else if !s.claimLineForSend(target) {
		if s.pickLine(target) == nil {
			writeJSON(w, 404, map[string]string{"error": "no alive line"})
		} else {
			writeJSON(w, 409, map[string]string{"error": "line busy"})
		}
		return
	}
	defer s.releaseLine(target)
	ln := s.pickLine(target)
	if ln == nil {
		writeJSON(w, 404, map[string]string{"error": "no alive line"})
		return
	}
	guid := s.newGUID()
	reply, err := s.sendUSSD(ln, req.Code)
	if err != nil {
		s.recordSend(ln.ID, false)
		s.fireWebhook(map[string]any{"type": "failed", "id": guid, "msg_type": "ussd", "line": ln.ID,
			"error": err.Error(), "channel": s.channelHealth(ln.ID), "time": time.Now()})
		writeJSON(w, 500, map[string]string{"id": guid, "error": err.Error(), "line": ln.ID})
		return
	}
	s.recordSend(ln.ID, true)
	s.fireWebhook(map[string]any{"type": "done", "id": guid, "msg_type": "ussd", "line": ln.ID,
		"reply": reply, "channel": s.channelHealth(ln.ID), "time": time.Now()})
	writeJSON(w, 200, map[string]string{"id": guid, "reply": reply, "line": ln.ID})
}

// hStatus reports a message's state by guid: status, fields, queue position (while queued,
// computed via COUNT — no stored position), and the channel's health. Unknown guid -> 404.
func (s *Server) hStatus(w http.ResponseWriter, r *http.Request) {
	guid := strings.TrimPrefix(r.URL.Path, "/status/")
	if guid == "" || len(guid) > 128 { // guid column is VARCHAR(64); cap the URL value as a sanity bound
		writeJSON(w, 400, map[string]string{"error": "need id"})
		return
	}
	db := s.DB()
	if db == nil {
		writeJSON(w, 404, map[string]string{"error": "no queue (db off)"})
		return
	}
	ot := s.outbox()
	var (
		id                                          int64
		line, typ, to, text, status, errCode, reply sql.NullString
		smsNo                                       sql.NullInt64
		createdAt                                   sql.NullTime
	)
	err := db.QueryRow("SELECT id, line, type, to_number, text, status, sms_no, error_code, reply, created_at FROM "+ot+
		" WHERE guid=? ORDER BY id DESC LIMIT 1", guid).
		Scan(&id, &line, &typ, &to, &text, &status, &smsNo, &errCode, &reply, &createdAt)
	if err == sql.ErrNoRows {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		s.elog.Printf("status query: %v", err)
		writeJSON(w, 500, map[string]string{"error": "db"})
		return
	}
	resp := map[string]any{"id": guid, "status": status.String, "type": typ.String, "line": line.String, "to": to.String}
	if text.Valid {
		resp["text"] = text.String
	}
	if reply.Valid {
		resp["reply"] = reply.String
	}
	if errCode.Valid {
		resp["error_code"] = errCode.String
	}
	if smsNo.Valid {
		resp["sms_no"] = smsNo.Int64
	}
	if createdAt.Valid {
		resp["queued_at"] = createdAt.Time
	}
	if status.String == "queued" { // position is only meaningful while waiting
		// One snapshot query (two separate COUNTs could read an inconsistent pair) with a checked error.
		var before, after sql.NullInt64
		if err := db.QueryRow("SELECT "+
			"SUM(CASE WHEN id < ? THEN 1 ELSE 0 END), "+
			"SUM(CASE WHEN id > ? THEN 1 ELSE 0 END) "+
			"FROM "+ot+" WHERE status='queued'", id, id).Scan(&before, &after); err != nil {
			s.elog.Printf("status position: %v", err)
		} else {
			resp["position"] = before.Int64 + 1
			resp["before"] = before.Int64
			resp["after"] = after.Int64
		}
	}
	if line.Valid && line.String != "" {
		resp["channel"] = s.channelHealth(line.String)
	}
	writeJSON(w, 200, resp)
}

// hCancel cancels a still-queued message by guid. Already sending/sent -> 409; unknown -> 404.
func (s *Server) hCancel(w http.ResponseWriter, r *http.Request) {
	guid := strings.TrimPrefix(r.URL.Path, "/message/")
	if guid == "" || len(guid) > 128 { // guid column is VARCHAR(64); cap the URL value as a sanity bound
		writeJSON(w, 400, map[string]string{"error": "need id"})
		return
	}
	db := s.DB()
	if db == nil {
		writeJSON(w, 404, map[string]string{"error": "no queue (db off)"})
		return
	}
	ot := s.outbox()
	res, err := db.Exec("UPDATE "+ot+" SET status='cancelled' WHERE guid=? AND status='queued'", guid)
	if err != nil {
		s.elog.Printf("cancel: %v", err)
		writeJSON(w, 500, map[string]string{"error": "db"})
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		writeJSON(w, 200, map[string]string{"id": guid, "status": "cancelled"})
		return
	}
	// Nothing cancelled: distinguish "already past queued" from "never existed".
	var status sql.NullString
	err = db.QueryRow("SELECT status FROM "+ot+" WHERE guid=? ORDER BY id DESC LIMIT 1", guid).Scan(&status)
	if err == sql.ErrNoRows {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "db"})
		return
	}
	writeJSON(w, 409, map[string]string{"error": "too late", "status": status.String})
}

func (s *Server) hInbox(w http.ResponseWriter, r *http.Request) {
	s.inboxMu.Lock()
	out := make([]Inbound, len(s.inbox))
	copy(out, s.inbox)
	s.inboxMu.Unlock()
	writeJSON(w, 200, out)
}

// hStats runs the 'status' command: returns the diagnostic snapshot inline AND emits the same body
// to the inbox table + webhook (so the answer is also waiting in the inbox, as via the DB path).
func (s *Server) hStats(w http.ResponseWriter, r *http.Request) {
	payload, _ := s.runCommand("status", "http", "")
	s.emitStats(payload)
	writeJSON(w, 200, payload)
}

// hReset runs the 'reset' command: cancels every still-queued outbox row and flushes in-RAM soft
// caches without restarting the service (see Server.reset). The result is returned inline and
// emitted to the inbox + webhook, identical to the DB path.
func (s *Server) hReset(w http.ResponseWriter, r *http.Request) {
	payload, _ := s.runCommand("reset", "http", "")
	s.emitStats(payload)
	writeJSON(w, 200, payload)
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
	// Report a LIVE db state: the pointer stays set after MySQL goes down, so ping (briefly) instead.
	dbOK := false
	if db := s.DB(); db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		dbOK = db.PingContext(ctx) == nil
	}
	writeJSON(w, 200, map[string]any{"ok": true, "lines": total, "alive": alive, "db": dbOK})
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

// fireQueued emits the "queued" webhook event (with channel health when a line is known).
func (s *Server) fireQueued(guid, msgType, line string, extra map[string]any) {
	if !s.markQueuedAnnounced(guid) {
		return
	}
	ev := map[string]any{"type": "queued", "id": guid, "msg_type": msgType, "line": line, "time": time.Now()}
	for k, v := range extra {
		ev[k] = v
	}
	if strings.TrimSpace(line) != "" {
		ev["channel"] = s.channelHealth(line)
	}
	s.fireWebhook(ev)
}

func (s *Server) markQueuedAnnounced(guid string) bool {
	guid = strings.TrimSpace(guid)
	if guid == "" {
		return true
	}
	now := time.Now()
	s.whMu.Lock()
	defer s.whMu.Unlock()
	if _, ok := s.queuedAnnounced[guid]; ok {
		return false
	}
	if len(s.queuedAnnounced) > webhookQueueCap {
		cutoff := now.Add(-24 * time.Hour)
		for id, at := range s.queuedAnnounced {
			if at.Before(cutoff) {
				delete(s.queuedAnnounced, id)
			}
		}
		if len(s.queuedAnnounced) > webhookQueueCap {
			s.queuedAnnounced = map[string]time.Time{}
		}
	}
	s.queuedAnnounced[guid] = now
	return true
}

const webhookQueueCap = 10000 // max pending webhook events held in RAM

// fireWebhook enqueues an event for reliable delivery (retried with exponential backoff up to
// webhook_retry.max_hours). A no-op when no webhook_url is configured.
func (s *Server) fireWebhook(payload map[string]any) {
	if s.cfg.WebhookURL == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	now := time.Now()
	s.whMu.Lock()
	if len(s.whQueue) >= webhookQueueCap {
		s.whMu.Unlock()
		s.elog.Printf("webhook queue full (%d), dropping event (saved to fallback)", webhookQueueCap)
		s.appendFallback(map[string]any{"kind": "webhook_drop_full", "payload": payload, "ts": now.Format(time.RFC3339)})
		return
	}
	s.whQueue = append(s.whQueue, &webhookEvent{payload: payload, body: body, firstAt: now, nextAt: now})
	s.whMu.Unlock()
}

// flushPendingWebhooks drains the in-RAM webhook queue to the fallback journal. Called once at
// shutdown (the worker has stopped) so events awaiting retry are recorded for replay rather than
// lost with the process. A delivery still in flight is also recorded — at worst the receiver gets
// it twice, which is safer than dropping it.
func (s *Server) flushPendingWebhooks() {
	s.whMu.Lock()
	pend := s.whQueue
	s.whQueue = nil
	s.whMu.Unlock()
	for _, e := range pend {
		s.appendFallback(map[string]any{"kind": "webhook_pending_shutdown", "payload": e.payload, "attempts": e.attempt, "ts": time.Now().Format(time.RFC3339)})
	}
	if len(pend) > 0 {
		log.Printf("flushed %d pending webhook(s) to fallback on shutdown", len(pend))
	}
}

// webhookWorker drains the in-RAM webhook queue: delivers due events, reschedules failures
// with exponential backoff (base_sec, then doubling), and drops events older than max_hours
// (recorded to the fallback journal). Delivery concurrency is bounded by webhookSem.
func (s *Server) webhookWorker(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		maxAge := time.Duration(s.cfg.WebhookRetry.MaxHours) * time.Hour
		budget := cap(s.webhookSem) // start at most this many deliveries per tick (bounds goroutines)
		var due []*webhookEvent
		var expired []map[string]any
		s.whMu.Lock()
		kept := s.whQueue[:0]
		for _, e := range s.whQueue {
			if now.Sub(e.firstAt) > maxAge && !e.inflight { // don't expire one that's mid-delivery — it may still 2xx
				expired = append(expired, map[string]any{"kind": "webhook_drop_expired", "payload": e.payload, "attempts": e.attempt, "ts": now.Format(time.RFC3339)})
				continue
			}
			if !e.inflight && !now.Before(e.nextAt) && len(due) < budget {
				e.inflight = true
				due = append(due, e)
			}
			kept = append(kept, e)
		}
		for i := len(kept); i < len(s.whQueue); i++ {
			s.whQueue[i] = nil // drop stale pointers in the tail so GC can reclaim removed events
		}
		s.whQueue = kept
		s.whMu.Unlock()
		for _, rec := range expired { // file I/O outside whMu so a slow disk can't stall the whole queue
			s.elog.Printf("webhook give up after %s (saved to fallback)", maxAge)
			s.appendFallback(rec)
		}
		for _, e := range due {
			// Acquire the slot HERE, not inside the goroutine: with a slow/dead receiver this paces the
			// worker instead of spawning a goroutine per due event that then piles up blocked on the
			// semaphore. Stay responsive to shutdown while waiting for a slot.
			select {
			case s.webhookSem <- struct{}{}:
				go s.deliverWebhook(e)
			case <-ctx.Done():
				return
			}
		}
	}
}

// deliverWebhook POSTs one event; on a 2xx it leaves the queue, otherwise it backs off. The caller
// (webhookWorker) holds one webhookSem slot for this delivery and we release it here. A panic in
// postWebhook is contained so it can neither crash the daemon (unrecovered goroutine panic) nor
// strand the slot/event.
func (s *Server) deliverWebhook(e *webhookEvent) {
	defer func() { <-s.webhookSem }()
	ok := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.elog.Printf("deliverWebhook panic: %v", r)
			}
		}()
		ok = s.postWebhook(e.body)
	}()
	s.whMu.Lock()
	defer s.whMu.Unlock()
	e.inflight = false
	if ok {
		for i, x := range s.whQueue {
			if x == e {
				s.whQueue = append(s.whQueue[:i], s.whQueue[i+1:]...)
				break
			}
		}
		return
	}
	e.attempt++
	delay := time.Duration(s.cfg.WebhookRetry.BaseSec) * time.Second
	for i := 1; i < e.attempt && delay < 6*time.Hour; i++ {
		delay *= 2 // base, 2*base, 4*base, ... (capped below to avoid int64 overflow at huge attempts)
	}
	if delay > 6*time.Hour {
		delay = 6 * time.Hour
	}
	e.nextAt = time.Now().Add(delay)
}

// postWebhook delivers the body and reports whether the receiver accepted it (HTTP 2xx).
func (s *Server) postWebhook(body []byte) bool {
	req, err := http.NewRequest("POST", s.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.WebhookToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.WebhookToken)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.elog.Printf("webhook post: %v", err)
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	s.logWebhookStatus(resp.StatusCode, resp.Header.Get("Location"))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

var stderrLog = log.New(os.Stderr, "", log.LstdFlags)

func (s *Server) logWebhookStatus(status int, location string) {
	ok := status >= 200 && status < 300
	msg := fmt.Sprintf("webhook OK %d", status)
	if !ok {
		msg = fmt.Sprintf("webhook WARN %d", status)
		if status >= 300 && status < 400 {
			msg = fmt.Sprintf("webhook WARN %d: expected 200, got %d — webhook_url redirects, the POST body is dropped, fix the URL", status, status)
			if location != "" {
				msg += ", Location: " + location
			}
		}
	}
	// Files get plain text only (never ANSI); OK lines stay out of .err.log.
	if ok {
		if s.fmain != nil {
			s.fmain.Print(msg)
		}
	} else if s.ferr != nil {
		s.ferr.Print(msg)
	}
	out := msg
	if s.coloredStatusTTY { // color goes to the live terminal only
		color := "\x1b[31m"
		if ok {
			color = "\x1b[32m"
		}
		out = color + msg + "\x1b[0m"
	}
	stderrLog.Print(out)
}

func startUpdateCheck(ctx context.Context, enabled bool) {
	if !enabled {
		log.Printf("проверка обновлений отключена (check_updates=false)")
		return
	}
	go func() {
		latest, err := fetchLatestVersion(ctx)
		if err != nil || latest == "" {
			return
		}
		if compareVersions(latest, appVersion) > 0 {
			printBox(log.Writer(), []string{
				"New goip-bridge release available",
				fmt.Sprintf("installed: v%s", trimVersionPrefix(appVersion)),
				fmt.Sprintf("latest:    v%s", trimVersionPrefix(latest)),
				appRepoURL + "/releases/latest",
			})
		}
	}()
}

func fetchLatestVersion(parent context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 2500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", latestAPIURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", appName+"/"+appVersion)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("github status %d", resp.StatusCode)
	}
	var v struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return "", err
	}
	return v.TagName, nil
}

func trimVersionPrefix(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

func compareVersions(a, b string) int {
	aa := versionParts(a)
	bb := versionParts(b)
	for i := 0; i < 3; i++ {
		if aa[i] > bb[i] {
			return 1
		}
		if aa[i] < bb[i] {
			return -1
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	v = trimVersionPrefix(v)
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, p := range strings.Split(v, ".") {
		if i >= len(out) {
			break
		}
		n, _ := strconv.Atoi(p)
		out[i] = n
	}
	return out
}

func runSelfUpdate() error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("-update supports the published linux/amd64 binary only (current: %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if realExe, err := filepath.EvalSymlinks(exe); err == nil {
		exe = realExe
	}
	info, err := os.Stat(exe)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	checksums, err := downloadBytes(client, latestAsset+"checksums.txt", 1<<20)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	want, err := checksumFor(checksums, appName)
	if err != nil {
		return err
	}
	bin, err := downloadBytes(client, latestAsset+appName, 128<<20)
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	sum := sha256.Sum256(bin)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s want %s", appName, got, want)
	}

	tmp := exe + ".new"
	bak := exe + ".bak"
	if err := os.WriteFile(tmp, bin, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, info.Mode().Perm()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := copyFile(exe, bak, info.Mode().Perm()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("backup %s: %w", bak, err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w (backup left at %s)", exe, err, bak)
	}
	fmt.Printf("updated %s from GitHub latest release\n", exe)
	if os.Geteuid() == 0 {
		if err := exec.Command("systemctl", "restart", appName).Run(); err != nil {
			// New binary is in place but not running — KEEP the backup so the operator can roll back.
			return fmt.Errorf("updated, but systemctl restart %s failed (backup kept at %s for rollback): %w", appName, bak, err)
		}
		fmt.Printf("systemd service %s restarted\n", appName)
		if err := os.Remove(bak); err != nil { // remove the backup only after a CONFIRMED successful restart
			fmt.Printf("note: updated and restarted, but could not remove backup %s: %v\n", bak, err)
		}
	} else {
		// Not root: we can't restart and confirm, so keep the backup for a manual rollback if needed.
		fmt.Printf("restart under systemd separately as root: systemctl restart %s\n", appName)
		fmt.Printf("backup of the previous binary kept at %s (remove it after the new one is confirmed running)\n", bak)
	}
	return nil
}

func downloadBytes(client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", appName+"/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func checksumFor(checksums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[1] == name {
			if len(f[0]) != sha256.Size*2 {
				return "", fmt.Errorf("bad checksum length for %s", name)
			}
			if _, err := hex.DecodeString(f[0]); err != nil {
				return "", fmt.Errorf("bad checksum for %s: %w", name, err)
			}
			return f[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("checksum for %s not found", name)
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

// weakHTTPToken reports whether the API token offers no real protection: empty, the shipped
// placeholder ("CHANGE_ME..."), or too short to resist guessing.
func weakHTTPToken(t string) bool {
	return t == "" || strings.HasPrefix(t, "CHANGE_ME") || len(t) < 16
}

// isLoopbackAddr reports whether a listen address is bound to loopback only.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false // ":8080" binds every interface
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost" // ParseIP doesn't resolve names; treat the literal as loopback
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
	b, err := json.Marshal(rec)
	if err != nil { // never emit a bare newline that loses the record — dump a readable form instead
		s.elog.Printf("fallback marshal: %v", err)
		b = []byte(fmt.Sprintf("%+v", rec))
	}
	if _, err := f.Write(append(b, '\n')); err != nil { // last-resort durable record — never lose it silently
		s.elog.Printf("fallback write %s: %v", s.fbPath, err)
		return
	}
	f.Sync() // recovery journal of last resort: flush to disk before returning so a crash can't drop it
}

func setupLogging(s *Server, cfgPath string) {
	dir := filepath.Dir(cfgPath)
	s.logDir = dir
	s.fbPath = filepath.Join(dir, "goip-bridge.fallback.jsonl")
	if configBoolDefault(s.cfg.ClearLogsStart, true) {
		preservePreviousLogs(dir)
	}
	max := int64(s.cfg.LogMaxMB) * 1024 * 1024
	mainCW := newCappedWriter(filepath.Join(dir, "goip-bridge.log"), max)
	errCW := newCappedWriter(filepath.Join(dir, "goip-bridge.err.log"), max)
	log.SetOutput(io.MultiWriter(os.Stderr, mainCW))
	s.fmain = log.New(mainCW, "", log.LstdFlags)
	s.ferr = log.New(io.MultiWriter(mainCW, errCW), "", log.LstdFlags)
	s.elog = log.New(io.MultiWriter(os.Stderr, mainCW, errCW), "", log.LstdFlags)
	s.coloredStatusTTY = stderrIsTTY()
	printBanner(io.MultiWriter(os.Stderr, mainCW)) // identity header (no timestamp) to screen + log file
	log.Printf("logging to %s (goip-bridge.log + .err.log, cap %d MB, debug=%v debug_line=%v clear_logs_on_start=%v)", dir, s.cfg.LogMaxMB, s.cfg.Debug, s.cfg.DebugLine, configBoolDefault(s.cfg.ClearLogsStart, true))
}

func configBoolDefault(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

func stderrIsTTY() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func preservePreviousLogs(dir string) {
	paths := []string{
		filepath.Join(dir, "goip-bridge.log"),
		filepath.Join(dir, "goip-bridge.err.log"),
	}
	if lineLogs, err := filepath.Glob(filepath.Join(dir, "goip-bridge.line-*.log")); err == nil {
		paths = append(paths, lineLogs...)
	}
	for _, path := range paths {
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			continue
		}
		prev := path + ".prev"
		_ = os.Remove(prev)
		if err := os.Rename(path, prev); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: cannot preserve previous log %s: %v\n", path, err)
		}
	}
}

// logEffectiveConfig prints the settings the daemon is actually running with (after
// defaults are applied, secrets masked) so the log shows exactly what was picked up.
func (s *Server) logEffectiveConfig() {
	c := s.cfg
	mask := func(v string) string {
		if v == "" {
			return "empty"
		}
		return "set"
	}
	row := func(name, value, desc string) { log.Printf("  %-15s %-24s %s", name, value, desc) }
	allow := "all (firewall only)"
	if len(c.AllowSrc) > 0 {
		allow = fmt.Sprintf("%v", c.AllowSrc)
	}
	lines := "all alive (round-robin)"
	if len(c.DefaultLines) > 0 {
		lines = fmt.Sprintf("%v", c.DefaultLines)
	}
	dp := c.SendPacing.Default

	log.Printf("config in effect (v%s) — applied values:", appVersion)
	row("listen_udp", c.ListenUDP, "UDP port GoIP lines register on")
	row("listen_http", c.ListenHTTP, "HTTP API bind address (/sms /ussd /status ...)")
	row("http_token", mask(c.HTTPToken), "Bearer required by the API (empty = open)")
	row("allow_src", allow, "source-IP filter for device packets")
	row("webhook_url", mask(c.WebhookURL), "POST target for inbound SMS + send results (empty = off)")
	row("webhook_token", mask(c.WebhookToken), "Bearer sent to the webhook")
	row("webhook_retry", fmt.Sprintf("max %dh, base %ds", c.WebhookRetry.MaxHours, c.WebhookRetry.BaseSec), "reliable webhook backoff window")
	row("fail_threshold", fmt.Sprintf("%d", c.FailThreshold), "consecutive send failures before line_failing")
	row("check_updates", fmt.Sprintf("%v", c.CheckUpdates), "startup GitHub release check")
	row("send_timeout", fmt.Sprintf("%ds", c.SendTimeout), "give up an SMS send after this")
	row("ussd_timeout", fmt.Sprintf("%ds", c.USSDTimeout), "give up a USSD session after this")
	row("ussd_retransmit", fmt.Sprintf("%ds", c.USSDRetransmit), "re-send USSD if no reply within this")
	row("line_dead", fmt.Sprintf("%ds", c.LineDeadSec), "line silent this long = dead (not routable)")
	row("send_pacing", fmt.Sprintf("%d-%ds", dp.MinSec, dp.MaxSec), "pause between sends on one line (default)")
	for id, r := range c.SendPacing.PerLine {
		row("  per_line["+id+"]", fmt.Sprintf("%d-%ds", r.MinSec, r.MaxSec), "per-line pacing override")
	}
	row("default_lines", lines, "lines for queue rows without a line")
	row("line_passwords", fmt.Sprintf("%d pinned", len(c.LinePasswords)), "per-line inbound password pins")
	row("debug", fmt.Sprintf("%v", c.Debug), "verbose SMS/USSD/inbound logging")
	row("debug_line", fmt.Sprintf("%v", c.DebugLine), "per-line raw keepalive logs (incl. password)")
	row("log_max_mb", fmt.Sprintf("%d", c.LogMaxMB), "per-file log cap (MB)")
	row("clear_logs", fmt.Sprintf("%v", configBoolDefault(c.ClearLogsStart, true)), "archive active bridge logs to .prev on startup")
	if c.DB != nil {
		row("db", fmt.Sprintf("%s:%d/%s", c.DB.Host, c.DB.Port, c.DB.Name),
			fmt.Sprintf("MySQL queue: inbox=%s outbox=%s poll=%ds (connecting...)", c.DB.InboxTable, c.DB.OutboxTable, c.DB.PollSec))
	} else {
		row("db", "off", "not configured — HTTP API / webhook only, no inbox/outbox queue")
	}
}

const lineLogMaxBytes = 3 * 1024 * 1024 // per-line keepalive log cap (debug_line); rotates to ".1"

var fnameUnsafeRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeFileToken makes a line id safe to embed in a filename (ids come straight
// from the device and are otherwise untrusted).
func safeFileToken(s string) string {
	t := fnameUnsafeRE.ReplaceAllString(s, "_")
	if t == "" || t == "." || t == ".." {
		t = "_"
	}
	if len(t) > 64 {
		t = t[:64]
	}
	return t
}

// lineLog returns (creating on first use) the per-line writer used when
// debug_line is on. Files sit next to the main log, one per line id, each capped
// at lineLogMaxBytes. They hold the RAW keepalive — INCLUDING the line password —
// so they are mode 0600 and meant for local debugging only.
func (s *Server) lineLog(id string) *cappedWriter {
	s.lineLogMu.Lock()
	defer s.lineLogMu.Unlock()
	if s.lineLogs == nil {
		s.lineLogs = map[string]*cappedWriter{}
	}
	key := safeFileToken(id) // key by the sanitized filename so two raw ids that collapse to one file share ONE writer
	w := s.lineLogs[key]
	if w == nil {
		w = newCappedWriter(filepath.Join(s.logDir, "goip-bridge.line-"+key+".log"), lineLogMaxBytes)
		s.lineLogs[key] = w
	}
	return w
}

func main() {
	cfgPath := flag.String("config", "config.json", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	doUpdate := flag.Bool("update", false, "download and install the latest release, then exit")
	initLang := flag.String("init", "", "create an annotated config at -config path and exit (value: ru or en)")
	flag.Parse()
	if *showVersion {
		printBanner(os.Stdout)
		return
	}
	if *doUpdate {
		printBanner(os.Stdout)
		if err := runSelfUpdate(); err != nil {
			log.Fatalf("update: %v", err)
		}
		return
	}

	// Self-create the config: explicit `-init ru|en`, or first run when the file is
	// missing (prompt on a terminal, otherwise print how to create it and exit).
	_, statErr := os.Stat(*cfgPath)
	missing := errors.Is(statErr, os.ErrNotExist)
	if *initLang != "" || missing {
		printBanner(os.Stderr)
		lang := strings.ToLower(*initLang)
		if lang == "" {
			if stdinIsTTY() {
				lang = promptConfigLang()
			}
			if lang == "" {
				fmt.Fprintf(os.Stderr, "config %q not found. Create it with one of:\n"+
					"  ./%s -config %s -init ru   # русские комментарии\n"+
					"  ./%s -config %s -init en   # English comments\n",
					*cfgPath, appName, *cfgPath, appName, *cfgPath)
				os.Exit(1)
			}
		}
		if lang != "ru" && lang != "en" {
			log.Fatalf("-init must be \"ru\" or \"en\" (got %q)", *initLang)
		}
		if err := writeDefaultConfig(*cfgPath, lang); err != nil {
			log.Fatalf("create config: %v", err)
		}
		fmt.Fprint(os.Stderr, afterCreateMsg(lang, *cfgPath))
		os.Exit(0)
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
		log.Fatalf("%v", err)
	}
	// Bind the HTTP listener here (not inside the goroutine): a bind failure is then reported in main
	// with normal exit, instead of a log.Fatalf inside httpLoop that would os.Exit and skip cleanup.
	httpLn, err := net.Listen("tcp", cfg.ListenHTTP)
	if err != nil {
		conn.Close()
		log.Fatalf("listen http %s: %v", cfg.ListenHTTP, err)
	}

	s := newServer(cfg)
	s.conn = conn
	setupLogging(s, *cfgPath)
	s.logEffectiveConfig()
	startUpdateCheck(ctx, cfg.CheckUpdates)

	if weakHTTPToken(cfg.HTTPToken) && !isLoopbackAddr(cfg.ListenHTTP) {
		log.Printf("WARNING: http_token is empty, a placeholder, or too short and the API listens on %s (non-loopback) — the send API is effectively OPEN to the network", cfg.ListenHTTP)
	}
	if len(cfg.allowNets) > 0 {
		log.Printf("device packets restricted to allow_src %v", cfg.AllowSrc)
	} else if len(cfg.LinePasswords) == 0 {
		log.Printf("WARNING: allow_src is empty and no line_passwords are pinned — any UDP source can register lines and inject inbound SMS/DLR; the firewall is the only barrier")
	}

	if cfg.DB != nil {
		if err := s.initDB(); err != nil {
			s.elog.Printf("db: configured but NOT connected (%s:%d/%s): %v — retrying in background; /sms and /ussd return 503 until connected",
				cfg.DB.Host, cfg.DB.Port, cfg.DB.Name, err)
			go s.dbConnectRetry(ctx)
		} else {
			log.Printf("db: connected to %s@%s:%d/%s — inbox table %q + outbox queue %q active (poll %ds)",
				cfg.DB.User, cfg.DB.Host, cfg.DB.Port, cfg.DB.Name, cfg.DB.InboxTable, cfg.DB.OutboxTable, cfg.DB.PollSec)
			s.reconcileSending()
			go s.outboxLoop(ctx)
		}
	} else {
		log.Printf("db: not configured — inbox/outbox queue disabled; /sms and /ussd send synchronously, inbound SMS go to the webhook only")
	}

	log.Printf("goip-bridge listening on UDP %s (GoIP lines register here)", cfg.ListenUDP)
	go s.httpLoop(ctx, httpLn)
	go s.udpLoop(ctx)
	go s.webhookWorker(ctx)
	go s.lineMonitor(ctx)

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
	drainWait := cfg.SendTimeout
	if cfg.USSDTimeout > drainWait { // a USSD send can run far longer than an SMS one
		drainWait = cfg.USSDTimeout
	}
	select {
	case <-drained:
	case <-time.After(time.Duration(drainWait+5) * time.Second):
		s.elog.Printf("shutdown: gave up waiting for in-flight sends")
	}
	// Close the UDP socket FIRST so no new inbound packet can spawn a background DB write, then drain the
	// in-flight ones (insertInbox / applyDLR / stat reply) so an SMS/DLR received in the last moment before
	// shutdown reaches the DB or the fallback journal instead of dying with its goroutine at db.Close().
	conn.Close()
	bgDeadline := time.Now().Add(time.Duration(drainWait+5) * time.Second)
	for s.bgWrites.Load() > 0 && time.Now().Before(bgDeadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := s.bgWrites.Load(); n > 0 {
		s.elog.Printf("shutdown: gave up waiting for %d background db write(s)", n)
	}
	// Webhook worker already stopped on ctx.Done — persist whatever is still pending in RAM to fallback.
	s.flushPendingWebhooks()
	if db := s.DB(); db != nil {
		db.Close()
	}
}

// ----------------------------------------------------------------------------
// Annotated config templates (JSONC). Emitted by `-init ru|en` / first run.
// The "db" block is commented out so one file covers both modes: leave it as is
// for HTTP-API/webhook only, or uncomment it to enable the MySQL outbox/inbox.
// ----------------------------------------------------------------------------

const configTemplateRU = `{
  // ══════════════════════════════════════════════════════════════
  //  goip-bridge — конфигурация (формат JSONC: разрешены коммен-
  //  тарии // и /* */ и висячие запятые). Пустое поле = значение
  //  по умолчанию. Конфиг разбит на блоки по назначению.
  // ══════════════════════════════════════════════════════════════

  // ──────────────────────────────────────────────────────────────
  //  БЛОК 1 · ПОДКЛЮЧЕНИЕ GoIP-ШЛЮЗОВ      (устройства → bridge, UDP)
  //  Сюда «прозваниваются» все линии всех GoIP.
  // ──────────────────────────────────────────────────────────────

  // UDP-порт, на котором bridge СЛУШАЕТ устройства. Этот же порт
  // пропишите в каждом GoIP как "SMS Server Port". ":44444" = все
  // интерфейсы, порт 44444. У всех GoIP адрес сервера один и тот же —
  // важно лишь, чтобы Client ID (id линии) НЕ повторялись.
  "listen_udp": ":44444",

  // С каких IP/подсетей ПРИНИМАТЬ пакеты от устройств (keepalive, SMS).
  // Пусто [] = принимать со ВСЕХ (барьер тогда только фаервол).
  // Если задать — пакет с любого ДРУГОГО IP, даже на правильный порт,
  // молча игнорируется. Это и есть «кому верим». Пример:
  //   "allow_src": ["192.168.1.0/24", "10.0.0.5"]
  // Если линия перечислена в line_passwords ниже, bridge проверяет пароль
  // входящих keepalive/SMS/DLR для этой линии. Если линия не перечислена,
  // пароль принимается из keepalive устройства. Поэтому allow_src и firewall
  // всё равно важны: они ограничивают, откуда вообще можно слать UDP-пакеты.
  "allow_src": [],

  // Пароли линий по их ID — работают в ОБЕ стороны:
  //   • ОТПРАВКА: bridge предъявляет этот пароль устройству (SMS/USSD).
  //   • ПРИЁМ: если линия здесь запинена, входящие пакеты (keepalive/SMS/DLR)
  //     с НЕсовпадающим паролем молча отбрасываются (линия не регистрируется).
  // Не запинена / пусто {} = «верить любому» паролю со шлюза (пароль просто
  // учится из keepalive). Пример:
  //   "line_passwords": {"Go1": "12345", "Go2": "qwerty"}
  "line_passwords": {},

  // ──────────────────────────────────────────────────────────────
  //  БЛОК 2 · ВХОДЯЩИЙ HTTP — наш API         (ваши клиенты → bridge)
  //  Отправка SMS/USSD, /lines, /inbox, /health.
  // ──────────────────────────────────────────────────────────────

  // Адрес, на котором bridge поднимает HTTP API.
  // "127.0.0.1:8080" = только локально (безопасно). Наружу — только
  // если задан http_token и вы понимаете риски.
  "listen_http": "127.0.0.1:8080",

  // Bearer-токен, который ВАШ клиент предъявляет bridge'у при запросе.
  // Пусто = API открыт всем. ОБЯЗАТЕЛЬНО задайте длинную случайную
  // строку, если API доступен по сети.
  "http_token": "CHANGE_ME_TO_LONG_RANDOM_TOKEN",

  // Примеры вызова API — отправка SMS на линию Go1 (подставьте http_token).
  // При ВКЛЮЧЁННОЙ БД отправка АСИНХРОННАЯ: ответ сразу (HTTP 202)
  //   {"status":"accepted","id":"<guid>","queued_at":<микротайм>},
  //   а результат (sent/failed + sms_no/reply) приходит ВЕБХУКОМ (Блок 3).
  //   Без БД — синхронно: {"line":"Go1","status":"sent","sms_no":123} (HTTP 200).
  // Любая живая линия: уберите "line" или поставьте "line":"".
  //
  //   curl:
  //     curl -X POST http://127.0.0.1:8080/sms \
  //       -H "Authorization: Bearer <http_token>" \
  //       -H "Content-Type: application/json" \
  //       -d '{"line":"Go1","to":"+996700000001","text":"Привет"}'
  //
  //   PHP (file_get_contents):
  //     $ctx = stream_context_create(["http" => [
  //       "method"  => "POST",
  //       "header"  => "Authorization: Bearer <http_token>\r\nContent-Type: application/json\r\n",
  //       "content" => json_encode(["line"=>"Go1","to"=>"+996700000001","text"=>"Привет"]),
  //       "timeout" => 10,
  //     ]]);
  //     $resp = @file_get_contents("http://127.0.0.1:8080/sms", false, $ctx);
  //     // проверка: $resp===false => ошибка сети; иначе json с "status":"sent"/"failed"
  //
  //   Node.js:
  //     await fetch("http://127.0.0.1:8080/sms", {
  //       method: "POST",
  //       headers: { "Authorization": "Bearer <http_token>", "Content-Type": "application/json" },
  //       body: JSON.stringify({ line: "Go1", to: "+996700000001", text: "Привет" }),
  //     });
  //
  //   USSD:    POST   /ussd  -d '{"line":"Go1","code":"*100#"}'   (тоже async при БД)
  //   Статус:  GET    /status/<id>    (статус, позиция в очереди, здоровье канала)
  //   Отмена:  DELETE /message/<id>   (если ещё в очереди → cancelled; иначе 409)
  //   Линии:   GET    /lines          (все эндпоинты — с тем же Bearer)
  //   Состоян: POST   /stats          (версия, аптайм, ОЗУ, линии, очередь — ответ ещё и во /inbox + вебхук)
  //   Сброс:   POST   /reset          (отменить все queued + сбросить кеши, без рестарта сервиса)

  // ──────────────────────────────────────────────────────────────
  //  БЛОК 3 · ИСХОДЯЩИЙ HTTP — вебхук          (bridge → ваш сервер)
  //  Bridge сам POST-ит вам входящие SMS и отчёты о доставке (DLR).
  // ──────────────────────────────────────────────────────────────

  // URL, куда bridge шлёт события {"type":"sms",...}, {"type":"dlr",...},
  // {"type":"queued",...}, {"type":"sent",...}, {"type":"failed",...},
  // {"type":"done",...} и события мониторинга линий: {"type":"line_down"}
  // (нет keepalive дольше line_dead_after_sec), {"type":"line_up"}
  // (линия восстановилась), {"type":"line_failing"} (fail_threshold ошибок
  // отправки подряд), {"type":"line_recovered"} (после line_failing отправка
  // снова прошла). Пусто = вебхук выключен. Если url задан, события отправки
  // шлются в ЛЮБОМ режиме — и с MySQL-очередью, и в синхронном без БД.
  // Редиректы НЕ выполняются: ответ 3xx считается ошибкой доставки (см. лог).
  "webhook_url": "",

  // Bearer-токен, который BRIDGE предъявляет ВАШЕМУ серверу (в заголовке
  // Authorization) — чтобы вы убедились, что запрос правда от bridge.
  "webhook_token": "",

  // Надёжная доставка вебхука: события держатся в ОЗУ и ретраятся с растущей
  // паузой (base_sec, дальше ×2: 5,10,20,40…), пока приёмник не ответит 2xx,
  // максимум max_hours. По умолчанию (если блока нет): 3ч / 5с.
  "webhook_retry": { "max_hours": 3, "base_sec": 5 },

  // Сколько ошибок отправки ПОДРЯД на одной линии считать проблемой канала:
  // на webhook_url уходит {"type":"line_failing"}, в /status линия помечается
  // suspect. Сбрасывается первой успешной отправкой. По умолчанию 10.
  "fail_threshold": 10,

  // ──────────────────────────────────────────────────────────────
  //  БЛОК 4 · ТАЙМАУТЫ ОТПРАВКИ              (bridge ↔ GoIP, секунды)
  // ──────────────────────────────────────────────────────────────

  // Сколько ждать подтверждения отправки SMS от GoIP. Не дождались →
  // SMS помечается 'failed' (ошибка "timeout"), БЕЗ автоповтора. По умолчанию 45.
  "send_timeout_sec": 45,

  // Сколько ВСЕГО ждать ответа на USSD-запрос. Не дождались →
  // вызов вернёт ошибку "ussd timeout". По умолчанию 120.
  "ussd_timeout_sec": 120,

  // Пока ждём ответ на USSD (до ussd_timeout_sec), КАЖДЫЕ столько секунд шлём
  // тот же USSD-запрос ПОВТОРНО — на случай если пакет потерялся (UDP без гарантий).
  // Повторы НЕ бесконечны: прекращаются, как только истечёт ussd_timeout_sec.
  // Должно быть МЕНЬШЕ ussd_timeout_sec. Слишком часто нельзя — рвёт USSD-сессию
  // у оператора, поэтому интервал большой. По умолчанию 60.
  "ussd_retransmit_sec": 60,

  // Темп рассылки в MySQL-очереди — пауза между заданиями на ОДНОМ канале.
  // Планировщик очереди держит на линии только одну SMS/USSD за раз. default — для всех линий;
  // per_line — переопределение по id линии (тот же Client ID, что в keepalive).
  // min==max = фиксированная пауза, 0/0 = без паузы, иначе случайная в [min,max] сек.
  // По умолчанию (если блока нет): 3–10с. per_line можно оставить пустым {} — тогда
  // на всех линиях действует default. Ключ = id линии, значение = {min_sec,max_sec}.
  // Раскомментируйте и поменяйте id под свои линии:
  "send_pacing": {
    "default":  { "min_sec": 3, "max_sec": 10 },
    "per_line": {
      // "Go1": { "min_sec": 5, "max_sec": 5  },   // фиксированные 5с между отправками на Go1
      // "Go2": { "min_sec": 1, "max_sec": 40 },   // случайная пауза 1–40с на Go2
      // "Go3": { "min_sec": 0, "max_sec": 0  }     // без паузы на Go3 (шлёт подряд)
    }
  },

  // Какие линии брать для строк очереди БЕЗ линии (line=NULL/''):
  // пусто [] = round-robin по всем живым; или список, напр. ["Go1","Go3"].
  "default_lines": [],

  // ──────────────────────────────────────────────────────────────
  //  БЛОК 5 · ЛОГИ / ДИАГНОСТИКА
  //  Логи goip-bridge.log + .err.log пишутся в ТУ ЖЕ папку, где лежит
  //  этот конфиг (рядом с файлом из -config). Значения ниже — дефолтные.
  // ──────────────────────────────────────────────────────────────

  "debug": false,              // true = подробный лог каждой SMS/USSD (номера, текст). По умолчанию false.
  "log_max_mb": 10,            // кап одного файла лога (goip-bridge.log/.err.log) в МБ.
                               //   По умолчанию 10 (если не указано/0). При debug:true
                               //   растёт быстро, потом ротация в .1.

  // true = для КАЖДОЙ линии свой файл goip-bridge.line-<id>.log с полным
  // «сырым» keepalive (ПАРОЛЬ, сигнал, IMEI/IMSI/ICCID, оператор, IP).
  // Каждый файл ≤ 3 МБ (потом ротация в .1). ⚠ содержит пароли — локально.
  "debug_line": false,

  // Через сколько секунд без keepalive линия считается «мёртвой» (alive=false)
  // и пропускается при отправке. По умолчанию 120.
  "line_dead_after_sec": 120,

  // true (по умолчанию) = при старте текущие логи bridge (goip-bridge.log,
  // .err.log, line-*.log) переезжают в одну копию .prev каждый — папка не
  // зарастает, а лог прошлого запуска (в т.ч. упавшего) сохраняется.
  "clear_logs_on_start": true,

  // true = при старте фоново спросить GitHub, не вышла ли новая версия
  // (один GET к api.github.com, до ~3с, при недоступности молча пропускается).
  // По умолчанию false — bridge никуда не «звонит».
  // Обновиться до свежего релиза: ./goip-bridge -update
  "check_updates": false,

  // ──────────────────────────────────────────────────────────────
  //  БЛОК 6 · MySQL (опционально) — очередь outbox + входящие inbox
  //  Раскомментируйте, чтобы включить. Без него bridge работает
  //  только через HTTP API (Блок 2) и/или вебхук (Блок 3).
  // ──────────────────────────────────────────────────────────────
  // "db": {
  //   "host": "127.0.0.1",
  //   "port": 3306,
  //   "user": "goip_bridge",
  //   "password": "CHANGE_ME",
  //   "name": "goip_go",
  //   "inbox_table": "goip_inbox",
  //   "outbox_table": "goip_outbox",
  //   "poll_sec": 3              // как часто опрашивать outbox на новые сообщения (по умолчанию 3)
  // }

  // ──────────────────────────────────────────────────────────────
  //  БЛОК 7 · ОТПРАВКА ЧЕРЕЗ ОЧЕРЕДЬ MySQL (Блок 6; это комментарий)
  //  HTTP-примеры (curl/PHP/Node) — выше, в Блоке 2.
  // ──────────────────────────────────────────────────────────────
  //
  //  Положить в очередь — просто INSERT в outbox (bridge заберёт сам). type='sms'|'ussd':
  //    SMS на конкретную линию:
  //      INSERT INTO goip_outbox (type, line, to_number, text, status)
  //      VALUES ('sms', 'Go1', '+996700000001', 'Привет', 'queued');
  //    SMS на любую живую (round-robin) — line = NULL:
  //      INSERT INTO goip_outbox (type, line, to_number, text, status)
  //      VALUES ('sms', NULL, '+996700000001', 'Привет', 'queued');
  //    USSD (баланс) — to_number = код, ответ потом в колонке reply:
  //      INSERT INTO goip_outbox (type, line, to_number, status)
  //      VALUES ('ussd', 'Go1', '*100#', 'queued');
  //    Статусы: queued -> sending -> sent->delivered (sms) | done (ussd) | failed | cancelled.
  //
  //  Управляющие команды (type='cmd') — ответ прилетает во ВХОДЯЩИЕ (goip_inbox, line='system') + вебхук:
  //    Состояние (аптайм, ОЗУ, линии, счётчики очереди):
  //      INSERT INTO goip_outbox (type, to_number, status) VALUES ('cmd', 'status', 'queued');
  //    Мягкий сброс (отменить все queued + сбросить кеши, БЕЗ рестарта сервиса; рут не нужен):
  //      INSERT INTO goip_outbox (type, to_number, status) VALUES ('cmd', 'reset', 'queued');
}
`

const configTemplateEN = `{
  // ══════════════════════════════════════════════════════════════
  //  goip-bridge — configuration (JSONC: // and /* */ comments and
  //  trailing commas are allowed). Empty field = default value.
  //  The config is split into blocks by purpose.
  // ══════════════════════════════════════════════════════════════

  // ──────────────────────────────────────────────────────────────
  //  BLOCK 1 · GoIP GATEWAY CONNECTION       (devices → bridge, UDP)
  //  Every line of every GoIP registers here.
  // ──────────────────────────────────────────────────────────────

  // UDP port the bridge LISTENS on for devices. Set the same port in
  // every GoIP as "SMS Server Port". ":44444" = all interfaces, port
  // 44444. All GoIPs may share the same server address — only the
  // Client ID (line id) must be unique.
  "listen_udp": ":44444",

  // Which IP/subnets to ACCEPT device packets (keepalive, SMS) from.
  // Empty [] = accept from ALL (firewall is then the only barrier).
  // If set, a packet from any OTHER IP — even on the right port — is
  // silently ignored. This is the "who we trust" filter. Example:
  //   "allow_src": ["192.168.1.0/24", "10.0.0.5"]
  // If a line is listed in line_passwords below, the bridge checks the
  // inbound keepalive/SMS/DLR password for that line. If a line is not listed,
  // its password is learned from the device keepalive. allow_src and the
  // firewall still matter: they limit who can send UDP packets at all.
  "allow_src": [],

  // Per-line passwords by ID — used BOTH ways:
  //   • SENDING: the bridge presents this password to the device (SMS/USSD).
  //   • RECEIVING: if a line is pinned here, inbound packets (keepalive/SMS/DLR)
  //     whose password does NOT match are silently dropped (line not registered).
  // Unpinned / empty {} = "trust any" password from the gateway (it is just
  // learned from keepalive). Example:
  //   "line_passwords": {"Go1": "12345", "Go2": "qwerty"}
  "line_passwords": {},

  // ──────────────────────────────────────────────────────────────
  //  BLOCK 2 · INBOUND HTTP — our API           (your clients → bridge)
  //  Send SMS/USSD, /lines, /inbox, /health.
  // ──────────────────────────────────────────────────────────────

  // Address the bridge serves the HTTP API on.
  // "127.0.0.1:8080" = local only (safe). Expose it only if http_token
  // is set and you understand the risks.
  "listen_http": "127.0.0.1:8080",

  // Bearer token YOUR client presents to the bridge on each request.
  // Empty = API open to everyone. MUST be a long random string if the
  // API is reachable over the network.
  "http_token": "CHANGE_ME_TO_LONG_RANDOM_TOKEN",

  // API call examples — send an SMS on line Go1 (use your http_token).
  // With a DB the send is ASYNC: the response is immediate (HTTP 202)
  //   {"status":"accepted","id":"<guid>","queued_at":<microtime>},
  //   and the result (sent/failed + sms_no/reply) arrives via WEBHOOK (Block 3).
  //   Without a DB it is synchronous: {"line":"Go1","status":"sent","sms_no":123} (HTTP 200).
  // Any alive line: omit "line" or set "line":"".
  //
  //   curl:
  //     curl -X POST http://127.0.0.1:8080/sms \
  //       -H "Authorization: Bearer <http_token>" \
  //       -H "Content-Type: application/json" \
  //       -d '{"line":"Go1","to":"+996700000001","text":"Hello"}'
  //
  //   PHP (file_get_contents):
  //     $ctx = stream_context_create(["http" => [
  //       "method"  => "POST",
  //       "header"  => "Authorization: Bearer <http_token>\r\nContent-Type: application/json\r\n",
  //       "content" => json_encode(["line"=>"Go1","to"=>"+996700000001","text"=>"Hello"]),
  //       "timeout" => 10,
  //     ]]);
  //     $resp = @file_get_contents("http://127.0.0.1:8080/sms", false, $ctx);
  //     // check: $resp===false => network error; otherwise JSON with "status":"sent"/"failed"
  //
  //   Node.js:
  //     await fetch("http://127.0.0.1:8080/sms", {
  //       method: "POST",
  //       headers: { "Authorization": "Bearer <http_token>", "Content-Type": "application/json" },
  //       body: JSON.stringify({ line: "Go1", to: "+996700000001", text: "Hello" }),
  //     });
  //
  //   USSD:    POST   /ussd  -d '{"line":"Go1","code":"*100#"}'   (async too with a DB)
  //   Status:  GET    /status/<id>    (status, queue position, channel health)
  //   Cancel:  DELETE /message/<id>   (still queued -> cancelled; otherwise 409)
  //   Lines:   GET    /lines          (all endpoints use the same Bearer)
  //   Stats:   POST   /stats          (version, uptime, RAM, lines, queue — reply also in /inbox + webhook)
  //   Reset:   POST   /reset          (cancel all queued + flush caches, no service restart)

  // ──────────────────────────────────────────────────────────────
  //  BLOCK 3 · OUTBOUND HTTP — webhook           (bridge → your server)
  //  The bridge POSTs inbound SMS and delivery reports (DLR) to you.
  // ──────────────────────────────────────────────────────────────

  // URL the bridge sends {"type":"sms",...}, {"type":"dlr",...},
  // {"type":"queued",...}, {"type":"sent",...}, {"type":"failed",...},
  // {"type":"done",...} and line-monitoring events to: {"type":"line_down"}
  // (no keepalive for longer than line_dead_after_sec), {"type":"line_up"}
  // (line recovered), {"type":"line_failing"} (fail_threshold consecutive
  // send failures), {"type":"line_recovered"} (a send succeeded again after
  // line_failing). Empty = webhook off. When the url is set, send events fire
  // in EVERY mode — both with the MySQL queue and in the synchronous no-DB mode.
  // Redirects are NOT followed: a 3xx response counts as a delivery failure (see log).
  "webhook_url": "",

  // Bearer token the BRIDGE presents to YOUR server (Authorization header)
  // so you can verify the request really came from the bridge.
  "webhook_token": "",

  // Reliable webhook delivery: events are held in RAM and retried with growing
  // backoff (base_sec, then doubling: 5,10,20,40…) until the receiver returns 2xx,
  // up to max_hours. Defaults (if the block is absent): 3h / 5s.
  "webhook_retry": { "max_hours": 3, "base_sec": 5 },

  // How many CONSECUTIVE send failures on one line count as a channel problem:
  // {"type":"line_failing"} is sent to webhook_url and the line is flagged
  // suspect in /status. Reset by the first successful send. Default 10.
  "fail_threshold": 10,

  // ──────────────────────────────────────────────────────────────
  //  BLOCK 4 · SEND TIMEOUTS                 (bridge ↔ GoIP, seconds)
  // ──────────────────────────────────────────────────────────────

  // How long to wait for the device to confirm an SMS send. On timeout the
  // SMS is marked 'failed' (error "timeout"), with NO auto-retry. Default 45.
  "send_timeout_sec": 45,

  // Total time to wait for a USSD reply. On timeout the call returns the
  // error "ussd timeout". Default 120.
  "ussd_timeout_sec": 120,

  // While waiting for the USSD reply (up to ussd_timeout_sec), re-send the same
  // USSD request every this many seconds — in case a packet was lost (UDP is
  // lossy). Retries are NOT infinite: they stop once ussd_timeout_sec elapses.
  // Must be LESS than ussd_timeout_sec. Too often breaks the operator's USSD
  // session, so keep it large. Default 60.
  "ussd_retransmit_sec": 60,

  // Send pacing in MySQL queue mode — pause between jobs on ONE channel.
  // The queue scheduler keeps only one SMS/USSD in flight per line. default applies to all
  // lines; per_line overrides by line id (the same Client ID seen in keepalive).
  // min==max = fixed pause, 0/0 = no pause, otherwise random in [min,max] sec.
  // Default (if the block is absent): 3-10s. per_line may stay empty {} — then
  // default applies to every line. Key = line id, value = {min_sec,max_sec}.
  // Uncomment and change the ids to match your lines:
  "send_pacing": {
    "default":  { "min_sec": 3, "max_sec": 10 },
    "per_line": {
      // "Go1": { "min_sec": 5, "max_sec": 5  },   // fixed 5s between sends on Go1
      // "Go2": { "min_sec": 1, "max_sec": 40 },   // random 1-40s on Go2
      // "Go3": { "min_sec": 0, "max_sec": 0  }     // no pause on Go3 (back-to-back)
    }
  },

  // Which lines to use for queue rows WITHOUT a line (line=NULL/''):
  // empty [] = round-robin over all alive lines; or a list, e.g. ["Go1","Go3"].
  "default_lines": [],

  // ──────────────────────────────────────────────────────────────
  //  BLOCK 5 · LOGS / DIAGNOSTICS
  //  Logs goip-bridge.log + .err.log are written to the SAME folder as
  //  this config file (next to the -config path). Values below are defaults.
  // ──────────────────────────────────────────────────────────────

  "debug": false,              // true = verbose per-SMS/USSD logging (numbers, text). Default false.
  "log_max_mb": 10,            // size cap per log file (goip-bridge.log/.err.log) in MB.
                               //   Default 10 (if unset/0). Grows fast with debug:true, then rotated to .1.

  // true = one file per line, goip-bridge.line-<id>.log, with the full raw
  // keepalive (PASSWORD, signal, IMEI/IMSI/ICCID, carrier, IP).
  // Each file is capped at 3 MB (then rotated to .1). WARNING: holds passwords — local only.
  "debug_line": false,

  // A line is "dead" after this many seconds without keepalive (alive=false)
  // and is skipped when sending. Default 120.
  "line_dead_after_sec": 120,

  // true (default) = on startup the current bridge logs (goip-bridge.log,
  // .err.log, line-*.log) are moved to a single .prev copy each — the folder
  // stays clean while the previous run's log (incl. a crashed one) is kept.
  "clear_logs_on_start": true,

  // true = on startup ask GitHub in the background whether a newer release
  // exists (one GET to api.github.com, up to ~3s, silently skipped when
  // unreachable). Default false — the bridge never "phones home".
  // To update to the latest release: ./goip-bridge -update
  "check_updates": false,

  // ──────────────────────────────────────────────────────────────
  //  BLOCK 6 · MySQL (optional) — outbox queue + inbox table
  //  Uncomment to enable. Without it the bridge works via the HTTP
  //  API (Block 2) and/or webhook (Block 3) only.
  // ──────────────────────────────────────────────────────────────
  // "db": {
  //   "host": "127.0.0.1",
  //   "port": 3306,
  //   "user": "goip_bridge",
  //   "password": "CHANGE_ME",
  //   "name": "goip_go",
  //   "inbox_table": "goip_inbox",
  //   "outbox_table": "goip_outbox",
  //   "poll_sec": 3              // how often to poll the outbox for new messages (default 3)
  // }

  // ──────────────────────────────────────────────────────────────
  //  BLOCK 7 · SENDING VIA THE MySQL QUEUE (Block 6; this is a comment)
  //  HTTP examples (curl/PHP/Node) are above, in Block 2.
  // ──────────────────────────────────────────────────────────────
  //
  //  Queue a message — just INSERT into outbox (the bridge picks it up). type='sms'|'ussd':
  //    SMS on a specific line:
  //      INSERT INTO goip_outbox (type, line, to_number, text, status)
  //      VALUES ('sms', 'Go1', '+996700000001', 'Hello', 'queued');
  //    SMS on any alive line (round-robin) — line = NULL:
  //      INSERT INTO goip_outbox (type, line, to_number, text, status)
  //      VALUES ('sms', NULL, '+996700000001', 'Hello', 'queued');
  //    USSD (balance) — to_number = code, the reply lands in the reply column:
  //      INSERT INTO goip_outbox (type, line, to_number, status)
  //      VALUES ('ussd', 'Go1', '*100#', 'queued');
  //    Statuses: queued -> sending -> sent->delivered (sms) | done (ussd) | failed | cancelled.
  //
  //  Control commands (type='cmd') — the reply arrives in the INBOX (goip_inbox, line='system') + webhook:
  //    Status (uptime, RAM, lines, queue counts):
  //      INSERT INTO goip_outbox (type, to_number, status) VALUES ('cmd', 'status', 'queued');
  //    Soft reset (cancel all queued + flush caches, NO service restart; no root needed):
  //      INSERT INTO goip_outbox (type, to_number, status) VALUES ('cmd', 'reset', 'queued');
}
`
