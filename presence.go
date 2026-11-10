package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Who is using this cactusdb, and what they are told about each other's
// changes. Every open page checks in every few seconds with an id of its own
// (one per tab). The answer lists the pages that have checked in lately, with
// their client IP, and the notices since the page last asked: a saved
// connection or query added, changed or deleted, a container deployed, an
// upgrade. A page isn't told about what it did itself.
//
// Polling, not a stream that stays open: a browser opens at most six
// connections to one server, and a stream per tab would use them up.
//
// Direct messages: a page can send a message to another open page, picked
// from the list. Pages are addressed by a public handle; the id a page checks
// in with stays secret, so only the page a message is sent to (and the one
// that sent it) is given it. Messages are kept in memory only, never logged,
// and are dropped once neither page is open.

const (
	presenceGone = 150 * time.Second // a page not heard from for this long has gone (a browser checks in from a background tab about once a minute)
	maxNotices   = 200
	maxPresence  = 1000
	maxMessages  = 2000 // kept in all
	maxMsgLen    = 2000 // characters in one message
	maxNameLen   = 40
)

var presenceIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

type presenceClient struct {
	id     string // secret: only the page knows it
	handle string // public: how other pages address it
	name   string // what the person calls themselves, optional
	ip     string
	agent  string
	since  time.Time
	seen   time.Time
	notice int64 // the last notice it was given
	msg    int64 // the last message it was given
}

// message is a direct message from one page to another.
type message struct {
	Seq      int64     `json:"seq"`
	At       time.Time `json:"at"`
	From     string    `json:"from"` // handles
	To       string    `json:"to"`
	FromIP   string    `json:"fromIp"`
	FromName string    `json:"fromName,omitempty"`
	Text     string    `json:"text"`
}

// notice is one change, shown to the other pages.
type notice struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	IP      string    `json:"ip,omitempty"` // who did it; "" = cactusdb itself
	Kind    string    `json:"kind"`         // connections, queries, containers or upgrade
	Text    string    `json:"text"`
	SavedID int64     `json:"savedId,omitempty"` // queries: of this saved connection
	from    string    // the page that did it: it isn't told
}

type presence struct {
	mu       sync.Mutex
	clients  map[string]*presenceClient
	notices  []notice
	seq      int64
	messages []message
	msgSeq   int64
}

// prune forgets the pages gone quiet. The caller holds p.mu.
func (p *presence) prune(now time.Time) {
	for id, c := range p.clients {
		if now.Sub(c.seen) > presenceGone {
			delete(p.clients, id)
			log.Printf("%s closed cactusdb or went quiet (%d using it now)", c.ip, len(p.clients))
		}
	}
	p.dropMessages()
}

// dropMessages forgets the messages neither of whose pages is open. The
// caller holds p.mu.
func (p *presence) dropMessages() {
	open := map[string]bool{}
	for _, c := range p.clients {
		open[c.handle] = true
	}
	kept := p.messages[:0]
	for _, m := range p.messages {
		if open[m.From] || open[m.To] {
			kept = append(kept, m)
		}
	}
	clear(p.messages[len(kept):])
	p.messages = kept
}

// presenceHandle is a page's public handle: from its secret id, which it
// doesn't give away.
func presenceHandle(id string) string {
	sum := sha256.Sum256([]byte("cactusdb presence handle\x00" + id))
	return hex.EncodeToString(sum[:8])
}

// byHandle finds an open page by its public handle. The caller holds p.mu.
func (p *presence) byHandle(h string) *presenceClient {
	for _, c := range p.clients {
		if c.handle == h {
			return c
		}
	}
	return nil
}

// cleanName is the name a person gave: one line, at most maxNameLen characters.
func cleanName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxNameLen {
		s = string(r[:maxNameLen])
	}
	return s
}

// notify tells every page except the one that sent r (if any).
func (s *server) notify(r *http.Request, kind string, savedID int64, format string, args ...any) {
	n := notice{At: time.Now(), Kind: kind, Text: fmt.Sprintf(format, args...), SavedID: savedID}
	if r != nil {
		n.IP, n.from = clientIP(r), r.Header.Get("X-Cactusdb-Client")
	}
	p := &s.presence
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	n.Seq = p.seq
	p.notices = append(p.notices, n)
	if len(p.notices) > maxNotices {
		p.notices = p.notices[len(p.notices)-maxNotices:]
	}
}

type presenceView struct {
	Handle string    `json:"handle"`
	Name   string    `json:"name,omitempty"`
	IP     string    `json:"ip"`
	Agent  string    `json:"agent"`
	Since  time.Time `json:"since"`
	Seen   time.Time `json:"seen"`
	You    bool      `json:"you,omitempty"`
}

// handlePresence is a page checking in: it answers who is using cactusdb and
// the notices the page hasn't had yet. A page's first check-in gets none.
func (s *server) handlePresence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Client string `json:"client"`
		Name   string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if !presenceIDRe.MatchString(req.Client) {
		writeErr(w, 400, errors.New("bad client id"))
		return
	}
	now := time.Now()
	p := &s.presence
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients == nil {
		p.clients = map[string]*presenceClient{}
	}
	p.prune(now)
	c := p.clients[req.Client]
	if c == nil {
		if len(p.clients) >= maxPresence {
			writeErr(w, 429, errors.New("too many pages are open"))
			return
		}
		// A page that went quiet and checks in again (a tab in the
		// background) gets the same handle back, and the messages sent
		// to it meanwhile.
		c = &presenceClient{id: req.Client, handle: presenceHandle(req.Client), ip: clientIP(r), agent: r.UserAgent(), since: now, notice: p.seq}
		p.clients[req.Client] = c
		log.Printf("%s opened cactusdb (%d using it now)", c.ip, len(p.clients))
	}
	c.seen, c.ip, c.name = now, clientIP(r), cleanName(req.Name)
	if u, ok := currentUser(r); ok && c.name == "" { // signed in (--remotelogin): known by name
		c.name = cleanName(u.Username)
	}
	out := []notice{}
	for _, n := range p.notices {
		if n.Seq > c.notice && n.from != c.id {
			out = append(out, n)
		}
	}
	c.notice = p.seq
	msgs := p.messagesFor(c)
	list := make([]presenceView, 0, len(p.clients))
	for _, x := range p.clients {
		list = append(list, presenceView{Handle: x.handle, Name: x.name, IP: x.ip, Agent: x.agent, Since: x.since, Seen: x.seen, You: x == c})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Since.Before(list[j].Since) })
	writeJSON(w, 200, map[string]any{"clients": list, "notices": out, "messages": msgs})
}

// messagesFor gives page c the messages to or from it that it hasn't had.
// The caller holds p.mu.
func (p *presence) messagesFor(c *presenceClient) []message {
	out := []message{}
	for _, m := range p.messages {
		if m.Seq > c.msg && (m.To == c.handle || m.From == c.handle) {
			out = append(out, m)
		}
	}
	c.msg = p.msgSeq
	return out
}

// handleMessage sends a direct message to one open page. Only that page, and
// the one that sent it, are given it; it is never logged.
func (s *server) handleMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Client string `json:"client"`
		To     string `json:"to"`
		Text   string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	switch {
	case req.Text == "":
		writeErr(w, 400, errors.New("the message is empty"))
		return
	case len([]rune(req.Text)) > maxMsgLen:
		writeErr(w, 400, fmt.Errorf("the message is too long (at most %d characters)", maxMsgLen))
		return
	}
	p := &s.presence
	p.mu.Lock()
	defer p.mu.Unlock()
	from := p.clients[req.Client]
	if from == nil {
		writeErr(w, 409, errors.New("this page isn't signed in to cactusdb's list yet: try again in a few seconds"))
		return
	}
	to := p.byHandle(req.To)
	switch {
	case to == nil:
		writeErr(w, 404, errors.New("that page has been closed: the message wasn't sent"))
		return
	case to == from:
		writeErr(w, 400, errors.New("that is this page"))
		return
	}
	p.msgSeq++
	m := message{Seq: p.msgSeq, At: time.Now(), From: from.handle, To: to.handle, FromIP: from.ip, FromName: from.name, Text: req.Text}
	p.messages = append(p.messages, m)
	if len(p.messages) > maxMessages {
		p.messages = p.messages[len(p.messages)-maxMessages:]
	}
	log.Printf("%s sent a message to %s", from.ip, to.ip) // not what it says
	writeJSON(w, 200, m)
}

// handlePresenceLeave is a page being closed or reloaded (sent as it goes).
func (s *server) handlePresenceLeave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Client string `json:"client"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	p := &s.presence
	p.mu.Lock()
	if c := p.clients[req.Client]; c != nil {
		delete(p.clients, req.Client)
		log.Printf("%s closed cactusdb (%d using it now)", c.ip, len(p.clients))
		p.dropMessages()
	}
	p.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
