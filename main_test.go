package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"
)

// TestFields covers the keepalive/RECEIVE field parser and its body extraction.
func TestFields(t *testing.T) {
	m, body := fields("id:Go1;password:secret;srcnum:+996555111222;msg:Привет мир")
	if m["id"] != "Go1" {
		t.Errorf("id=%q", m["id"])
	}
	if m["srcnum"] != "+996555111222" {
		t.Errorf("srcnum=%q", m["srcnum"])
	}
	if body != "Привет мир" {
		t.Errorf("body=%q", body)
	}
}

// TestFieldsMsgBoundary guards the ";msg:" boundary fix: a value of an earlier field that contains
// "msg:" must NOT be mistaken for the start of the body (the old strings.Index(s,"msg:") did).
func TestFieldsMsgBoundary(t *testing.T) {
	m, body := fields("id:SIM1;srcnum:+79001234567;z:abcmsg:INJECT;msg:real")
	if m["id"] != "SIM1" {
		t.Errorf("id=%q", m["id"])
	}
	if body != "real" {
		t.Errorf("body=%q want %q (earlier msg: must not be the boundary)", body, "real")
	}
	if _, b := fields("msg:hello"); b != "hello" { // leading msg: with no preceding field
		t.Errorf("leading msg body=%q", b)
	}
	if _, b := fields("id:X;msg:send msg:later"); b != "send msg:later" { // msg: legitimately in the body
		t.Errorf("body with msg: inside =%q", b)
	}
}

func TestSanitizeProto(t *testing.T) {
	if got := sanitizeProto("foo\r\nDONE 1"); got != "fooDONE 1" {
		t.Errorf("sanitizeProto stripped wrong: %q", got)
	}
}

func TestValidNumber(t *testing.T) {
	for _, g := range []string{"+996700000001", "996700000001", "100"} {
		if !validNumber(g) {
			t.Errorf("validNumber(%q)=false", g)
		}
	}
	for _, b := range []string{"", "+12", "12 34", "abc", "+1234567890123456789012"} {
		if validNumber(b) {
			t.Errorf("validNumber(%q)=true", b)
		}
	}
}

func TestValidIdent(t *testing.T) {
	if !validIdent("Go1") {
		t.Error("Go1 should be valid")
	}
	if validIdent("1Go") || validIdent("go-1") {
		t.Error("leading digit / hyphen should be invalid")
	}
}

// TestIsLoopbackAddr also covers the "localhost" literal that net.ParseIP can't resolve.
func TestIsLoopbackAddr(t *testing.T) {
	for _, a := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		if !isLoopbackAddr(a) {
			t.Errorf("isLoopbackAddr(%q)=false", a)
		}
	}
	for _, a := range []string{"0.0.0.0:8080", ":8080", "192.168.1.5:8080"} {
		if isLoopbackAddr(a) {
			t.Errorf("isLoopbackAddr(%q)=true", a)
		}
	}
}

func TestWeakHTTPToken(t *testing.T) {
	for _, w := range []string{"", "CHANGE_ME_TO_LONG_RANDOM_TOKEN", "short", "0123456789abcde"} {
		if !weakHTTPToken(w) {
			t.Errorf("weakHTTPToken(%q)=false", w)
		}
	}
	for _, s := range []string{"0123456789abcdef0123456789", "a-very-long-random-token-xyz"} {
		if weakHTTPToken(s) {
			t.Errorf("weakHTTPToken(%q)=true", s)
		}
	}
}

func TestStripJSONComments(t *testing.T) {
	in := []byte("{\n  // line comment\n  \"a\": 1, /* block */\n  \"b\": \"http://x\",\n}")
	var v map[string]any
	if err := json.Unmarshal(stripJSONComments(in), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v["a"] != float64(1) {
		t.Errorf("a=%v", v["a"])
	}
	if v["b"] != "http://x" { // the // inside a string must survive
		t.Errorf("b=%v", v["b"])
	}
}

// TestLoadConfigNegativeTimings verifies negative timing values fall back to defaults instead of
// reaching time.NewTicker/NewTimer (a non-positive duration panics the ticker).
func TestLoadConfigNegativeTimings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	cfg := `{"send_timeout_sec":-5,"ussd_timeout_sec":-1,"ussd_retransmit_sec":-1,` +
		`"log_max_mb":-1,"line_dead_after_sec":-1,` +
		`"db":{"host":"h","user":"u","password":"p","name":"n","poll_sec":-9}}`
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"SendTimeout", c.SendTimeout, 45},
		{"USSDTimeout", c.USSDTimeout, 120},
		{"USSDRetransmit", c.USSDRetransmit, 60},
		{"LogMaxMB", c.LogMaxMB, 10},
		{"LineDeadSec", c.LineDeadSec, 120},
		{"DB.PollSec", c.DB.PollSec, 3},
	} {
		if tc.got != tc.want {
			t.Errorf("%s=%d want %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestSeenRecently(t *testing.T) {
	s := newServer(&Config{})
	if s.seenRecently("R:Go1:1") {
		t.Error("first occurrence should not be a dup")
	}
	if !s.seenRecently("R:Go1:1") {
		t.Error("repeat should be a dup")
	}
	if s.seenRecently("R:Go1:2") {
		t.Error("a different key should not be a dup")
	}
}

// TestSeenRecentlyEviction verifies the time-based purge removes keys older than the window even at
// low traffic (the old size-only purge kept them indefinitely, risking a false duplicate-drop).
func TestSeenRecentlyEviction(t *testing.T) {
	s := newServer(&Config{})
	s.seen["R:old:1"] = time.Now().Add(-(seenWindow + time.Minute))
	s.seenPurge = time.Now().Add(-2 * time.Minute) // force the once-a-minute purge to run now
	s.seenRecently("R:new:1")
	if _, ok := s.seen["R:old:1"]; ok {
		t.Error("stale key should have been evicted by the time-based purge")
	}
}

func TestPacingDelay(t *testing.T) {
	neg := newServer(&Config{SendPacing: &SendPacing{Default: PacingRange{MinSec: -5, MaxSec: -1}, PerLine: map[string]PacingRange{}}})
	if d := neg.pacingDelay("x"); d != 0 {
		t.Errorf("non-positive max should yield 0, got %v", d)
	}
	fixed := newServer(&Config{SendPacing: &SendPacing{Default: PacingRange{MinSec: 5, MaxSec: 5}, PerLine: map[string]PacingRange{}}})
	if d := fixed.pacingDelay("x"); d != 5*time.Second {
		t.Errorf("fixed 5s expected, got %v", d)
	}
}

// TestSafeFileToken confirms multibyte input is sanitized to ASCII before the 64-byte cap, so the
// result is always valid UTF-8 (the truncation can never split a rune — there are none left).
func TestSafeFileToken(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "ΩΨ"
	}
	tok := safeFileToken(long)
	if len(tok) > 64 || !utf8.ValidString(tok) {
		t.Errorf("token len=%d valid=%v: %q", len(tok), utf8.ValidString(tok), tok)
	}
	if safeFileToken("") != "_" {
		t.Error("empty -> _")
	}
	if got := safeFileToken("a/b\\c"); got != "a_b_c" {
		t.Errorf("safeFileToken(a/b\\c)=%q", got)
	}
}

// TestHLinesDistinct guards against the (claimed but non-existent) /lines pointer-aliasing bug:
// cp := *ln is a fresh variable each iteration, so every line must come back with its own id.
func TestHLinesDistinct(t *testing.T) {
	s := newServer(&Config{LineDeadSec: 120})
	now := time.Now()
	for _, id := range []string{"Go1", "Go2", "Go3"} {
		s.lines[id] = &Line{ID: id, Alive: true, LastSeen: now}
	}
	w := httptest.NewRecorder()
	s.hLines(w, httptest.NewRequest("GET", "/lines", nil))
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3", len(got))
	}
	ids := map[string]bool{}
	for _, l := range got {
		ids[l["id"].(string)] = true
		if !l["alive"].(bool) {
			t.Errorf("line %v should be alive", l["id"])
		}
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 distinct ids, got %v (aliasing regression)", ids)
	}
}
