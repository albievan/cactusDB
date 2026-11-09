package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
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

const (
	presenceGone = 75 * time.Second // a page not heard from for this long has gone (a hidden tab checks in slowly)
	maxNotices   = 200
	maxPresence  = 1000
)

var presenceIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

type presenceClient struct {
	id     string
	ip     string
	agent  string
	since  time.Time
	seen   time.Time
	notice int64 // the last notice it was given
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
	mu      sync.Mutex
	clients map[string]*presenceClient
	notices []notice
	seq     int64
}

// prune forgets the pages gone quiet. The caller holds p.mu.
func (p *presence) prune(now time.Time) {
	for id, c := range p.clients {
		if now.Sub(c.seen) > presenceGone {
			delete(p.clients, id)
			log.Printf("%s closed cactusdb or went quiet (%d using it now)", c.ip, len(p.clients))
		}
	}
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
	IP    string    `json:"ip"`
	Agent string    `json:"agent"`
	Since time.Time `json:"since"`
	Seen  time.Time `json:"seen"`
	You   bool      `json:"you,omitempty"`
}

// handlePresence is a page checking in: it answers who is using cactusdb and
// the notices the page hasn't had yet. A page's first check-in gets none.
func (s *server) handlePresence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Client string `json:"client"`
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
		c = &presenceClient{id: req.Client, ip: clientIP(r), agent: r.UserAgent(), since: now, notice: p.seq}
		p.clients[req.Client] = c
		log.Printf("%s opened cactusdb (%d using it now)", c.ip, len(p.clients))
	}
	c.seen, c.ip = now, clientIP(r)
	out := []notice{}
	for _, n := range p.notices {
		if n.Seq > c.notice && n.from != c.id {
			out = append(out, n)
		}
	}
	c.notice = p.seq
	list := make([]presenceView, 0, len(p.clients))
	for _, x := range p.clients {
		list = append(list, presenceView{IP: x.ip, Agent: x.agent, Since: x.since, Seen: x.seen, You: x == c})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Since.Before(list[j].Since) })
	writeJSON(w, 200, map[string]any{"clients": list, "notices": out})
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
	}
	p.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
