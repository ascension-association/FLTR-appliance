// Package api serves gk-fltr's HTTPS JSON API for the companion app.
//
// The API is for the home network only (see HomeNet): connections from
// elsewhere are closed before TLS, whatever address it listens on. Every
// request but /pair needs "Authorization: Bearer <token>". The server uses
// a self-signed certificate that apps trust by its pin. Apps get both by
// scanning the pairing QR code (see Credentials.PairingURI), or, until the
// first app pairs, from /pair (see Config.AutoPair). docs/API.md describes
// the endpoints.
package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ascension-association/FLTR/internal/blocklog"
	"github.com/ascension-association/FLTR/internal/policy"
	"github.com/ascension-association/FLTR/internal/quota"
	"github.com/ascension-association/FLTR/internal/settings"
)

// Status is the part of GET /status that the caller knows about.
type Status struct {
	Mode       string    `json:"mode"` // "queue" or "log"
	FailClosed bool      `json:"fail_closed"`
	Protocols  []string  `json:"protocols"`
	Started    time.Time `json:"started"`
}

// Config configures a Server.
type Config struct {
	Credentials  *Credentials
	SettingsPath string
	Tracker      *quota.Tracker
	// Policy returns the policy in effect.
	Policy func() *policy.Policy
	// Apply builds the policy with s in place of the settings file and puts
	// it into effect, or returns why it can't.
	Apply    func(s *settings.Settings) error
	Status   func() Status
	BlockLog string // block log path, if any
	// Home reports whether a client address is on the home network
	// (default: a HomeNet).
	Home func(netip.Addr) bool
	Now  func() time.Time
	// AutoPair lets the first app on the home network get the token from
	// POST /pair, without the QR code, until an app has paired.
	AutoPair bool
	// Name is shown to apps looking for a box to pair with.
	Name string
}

// Server is the API server.
type Server struct {
	cfg     Config
	handler http.Handler

	mu sync.Mutex // serializes changes to settings and the tracker

	seenMu sync.Mutex
	seen   map[string]*Device

	failMu sync.Mutex
	fails  map[netip.Addr]*failWindow

	pairMu   sync.Mutex
	pairOpen atomic.Bool // no app has paired with the token yet

	servers []*http.Server
}

const (
	maxSeen      = 512
	maxFails     = 10 // failed authentications per address per minute
	maxBody      = 64 << 10
	maxBlocks    = 1000
	blockLogTail = 1 << 20
)

type failWindow struct {
	start time.Time
	n     int
}

// New returns a Server.
func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Home == nil {
		cfg.Home = (&HomeNet{}).Contains
	}
	s := &Server{cfg: cfg, seen: map[string]*Device{}, fails: map[netip.Addr]*failWindow{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/users", s.listUsers)
	mux.HandleFunc("GET /api/v1/users/{name}", s.getUser)
	mux.HandleFunc("PUT /api/v1/users/{name}", s.putUser)
	mux.HandleFunc("DELETE /api/v1/users/{name}", s.deleteUser)
	mux.HandleFunc("POST /api/v1/users/{name}/extra", s.addExtra)
	mux.HandleFunc("PUT /api/v1/users/{name}/pause", s.pause)
	mux.HandleFunc("DELETE /api/v1/users/{name}/pause", s.pause)
	mux.HandleFunc("GET /api/v1/devices", s.devices)
	mux.HandleFunc("GET /api/v1/blocks", s.blocks)
	s.pairOpen.Store(cfg.Credentials.PairOpen())
	root := http.NewServeMux()
	root.HandleFunc("GET /api/v1/pair", s.pairGet)
	root.HandleFunc("POST /api/v1/pair", s.pairPost)
	root.Handle("/", s.auth(mux))
	s.handler = s.homeOnly(root)
	return s
}

// PairOpen reports whether an app can pair now without the QR code.
func (s *Server) PairOpen() bool { return s.cfg.AutoPair && s.pairOpen.Load() }

// Handler returns the API's HTTP handler, without TLS.
func (s *Server) Handler() http.Handler { return s.handler }

// Listen serves the API over TLS on each address. Listening errors are
// returned at once; later errors are logged.
func (s *Server) Listen(addrs []string) error {
	var lns []net.Listener
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a)
		if err != nil {
			for _, l := range lns {
				l.Close()
			}
			return fmt.Errorf("api: %w", err)
		}
		lns = append(lns, homeListener{ln, s.cfg.Home})
	}
	for _, ln := range lns {
		hs := &http.Server{
			Handler:           s.handler,
			TLSConfig:         &tls.Config{Certificates: []tls.Certificate{s.cfg.Credentials.Cert}, MinVersion: tls.VersionTLS12},
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
			MaxHeaderBytes:    16 << 10,
			ErrorLog:          log.New(io.Discard, "", 0), // TLS handshake noise from scanners
		}
		s.servers = append(s.servers, hs)
		go func() {
			if err := hs.ServeTLS(ln, "", ""); !errors.Is(err, http.ErrServerClosed) {
				log.Printf("api %s: %v", ln.Addr(), err)
			}
		}()
	}
	return nil
}

// Close stops the servers.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, hs := range s.servers {
		hs.Shutdown(ctx)
	}
	return nil
}

// homeOnly refuses requests from outside the home network.
func (s *Server) homeOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Home(clientIP(r)) {
			writeError(w, http.StatusForbidden, "the API is only available on the home network")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) netip.Addr {
	ap, _ := netip.ParseAddrPort(r.RemoteAddr)
	return ap.Addr().Unmap()
}

// auth checks the bearer token, refusing addresses with too many failures.
func (s *Server) auth(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.cfg.Credentials.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		now := s.cfg.Now()

		s.failMu.Lock()
		f := s.fails[ip]
		if f != nil && now.Sub(f.start) > time.Minute {
			delete(s.fails, ip)
			f = nil
		}
		limited := f != nil && f.n >= maxFails
		s.failMu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too many failed attempts")
			return
		}

		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			s.failMu.Lock()
			if f = s.fails[ip]; f == nil {
				if len(s.fails) > 4096 {
					clear(s.fails)
				}
				f = &failWindow{start: now}
				s.fails[ip] = f
			}
			f.n++
			if f.n == maxFails {
				log.Printf("api: %d failed authentications from %s; refusing it for a minute", f.n, ip)
			}
			s.failMu.Unlock()
			writeError(w, http.StatusUnauthorized, "missing or wrong token")
			return
		}
		if s.pairOpen.Load() {
			// An app paired with the QR code: nobody else may pair
			// without it.
			s.closePairing(ip, "the QR code")
		}
		next.ServeHTTP(w, r)
	})
}

// pairGet tells an app looking for a box whether it can pair without the
// QR code.
func (s *Server) pairGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"fltr": true, "name": s.cfg.Name, "open": s.PairOpen()})
}

// pairPost gives the token to the first app that asks while no app has
// paired, then closes pairing for good: only a new token (-unpair) opens
// it again. The app learns the certificate pin from this connection.
func (s *Server) pairPost(w http.ResponseWriter, r *http.Request) {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	if !s.PairOpen() {
		writeError(w, http.StatusConflict, "an app has already paired; scan the QR code from fltr's log instead")
		return
	}
	if err := s.closePairingLocked(clientIP(r), "auto-pairing"); err != nil {
		writeError(w, http.StatusInternalServerError, "can't record the pairing: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": s.cfg.Credentials.Token})
}

func (s *Server) closePairing(ip netip.Addr, how string) {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	if err := s.closePairingLocked(ip, how); err != nil {
		log.Printf("api: %v", err)
	}
}

// closePairingLocked records that the app at ip paired, before anything
// else learns so, so a crash can't let a second app pair.
func (s *Server) closePairingLocked(ip netip.Addr, how string) error {
	if !s.pairOpen.Load() {
		return nil
	}
	if err := s.cfg.Credentials.closePairing(); err != nil {
		return fmt.Errorf("closing pairing: %w", err)
	}
	s.pairOpen.Store(false)
	log.Printf("app API: the app at %s paired using %s; apps can now pair only with the QR code", ip, how)
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	now := s.cfg.Now()
	st := s.cfg.Status()
	p := s.cfg.Policy()
	writeJSON(w, http.StatusOK, struct {
		Status
		Time          time.Time      `json:"time"`
		UptimeSeconds int64          `json:"uptime_seconds"`
		NextReset     time.Time      `json:"next_reset"`
		Rules         map[string]int `json:"rules"`
		Users         int            `json:"users"`
	}{
		Status:        st,
		Time:          now,
		UptimeSeconds: int64(now.Sub(st.Started) / time.Second),
		NextReset:     s.cfg.Tracker.PeriodStart(now).AddDate(0, 0, 1),
		Rules:         p.Counts(),
		Users:         len(p.Users()),
	})
}

// User is a user as the API reports it.
type User struct {
	Name             string       `json:"name"`
	AllowanceSeconds int64        `json:"allowance_seconds"`
	UsedSeconds      int64        `json:"used_seconds"`
	ExtraSeconds     int64        `json:"extra_seconds"`
	RemainingSeconds int64        `json:"remaining_seconds"`
	Paused           bool         `json:"paused"`
	Managed          bool         `json:"managed"` // has an entry in the settings file
	Devices          []UserDevice `json:"devices"`
}

// UserDevice is one of a user's devices.
type UserDevice struct {
	Type   string `json:"type"` // "ip" or "mac"
	Value  string `json:"value"`
	Source string `json:"source"` // "settings" (the app) or "rules" (the rules file)
}

func (s *Server) users() ([]User, error) {
	set, err := settings.Load(s.cfg.SettingsPath)
	if err != nil {
		return nil, err
	}
	now := s.cfg.Now()
	var us []User
	for _, pu := range s.cfg.Policy().Users() {
		g := "user:" + pu.Name
		use := s.cfg.Tracker.Usage(g, now)
		su, managed := set.Users[pu.Name]
		u := User{
			Name:             pu.Name,
			AllowanceSeconds: secs(pu.Allowance),
			UsedSeconds:      secs(use.Used),
			ExtraSeconds:     secs(use.Extra),
			RemainingSeconds: secs(s.cfg.Tracker.Remaining(g, pu.Allowance, now)),
			Paused:           use.Paused,
			Managed:          managed,
			Devices:          []UserDevice{},
		}
		for _, rule := range pu.Rules {
			kind, value, _ := strings.Cut(rule, " ")
			src := "rules"
			if slices.Contains(su.Devices, settings.Device{Type: kind, Value: value}) {
				src = "settings"
			}
			u.Devices = append(u.Devices, UserDevice{kind, value, src})
		}
		us = append(us, u)
	}
	return us, nil
}

func secs(d time.Duration) int64 { return int64(d / time.Second) }

func (s *Server) user(name string) (User, bool, error) {
	us, err := s.users()
	if err != nil {
		return User{}, false, err
	}
	for _, u := range us {
		if u.Name == name {
			return u, true, nil
		}
	}
	return User{}, false, nil
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.users()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if us == nil {
		us = []User{}
	}
	writeJSON(w, http.StatusOK, us)
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	s.respondUser(w, r.PathValue("name"))
}

func (s *Server) respondUser(w http.ResponseWriter, name string) {
	u, ok, err := s.user(name)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	case !ok:
		writeError(w, http.StatusNotFound, "no user "+strconv.Quote(name))
	default:
		writeJSON(w, http.StatusOK, u)
	}
}

// changeSettings applies change to the settings file and puts the result
// into effect. Called with s.mu held.
func (s *Server) changeSettings(change func(*settings.Settings) error) (code int, err error) {
	cur, err := settings.Load(s.cfg.SettingsPath)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	next := cur.Clone()
	if err := change(next); err != nil {
		return http.StatusBadRequest, err
	}
	if err := s.cfg.Apply(next); err != nil {
		return http.StatusBadRequest, err
	}
	if err := next.Save(s.cfg.SettingsPath); err != nil {
		if aerr := s.cfg.Apply(cur); aerr != nil {
			log.Printf("api: restoring previous settings: %v", aerr)
		}
		return http.StatusInternalServerError, fmt.Errorf("saving settings: %w", err)
	}
	return http.StatusOK, nil
}

func (s *Server) putUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body settings.User
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	code, err := s.changeSettings(func(set *settings.Settings) error {
		if err := policy.CheckUser(name); err != nil {
			return err
		}
		if a := body.AllowanceSeconds; a != nil && (*a < 0 || *a > 86400) {
			return fmt.Errorf("allowance_seconds %d out of range (0 to 86400)", *a)
		}
		if body.AllowanceSeconds == nil && len(body.Devices) == 0 {
			return errors.New("a user needs allowance_seconds or at least one device")
		}
		u := settings.User{AllowanceSeconds: body.AllowanceSeconds, Devices: []settings.Device{}}
		for _, d := range body.Devices {
			if d.Type != "ip" && d.Type != "mac" {
				return fmt.Errorf("invalid device type %q (want ip or mac)", d.Type)
			}
			v, err := policy.Normalize(d.Type, d.Value)
			if err != nil {
				return err
			}
			if nd := (settings.Device{Type: d.Type, Value: v}); !slices.Contains(u.Devices, nd) {
				u.Devices = append(u.Devices, nd)
			}
		}
		set.Users[name] = u
		return nil
	})
	s.mu.Unlock()
	if err != nil {
		writeError(w, code, err.Error())
		return
	}
	s.respondUser(w, name)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	missing := false
	code, err := s.changeSettings(func(set *settings.Settings) error {
		if _, ok := set.Users[name]; !ok {
			missing = true
			return nil
		}
		delete(set.Users, name)
		return nil
	})
	s.mu.Unlock()
	switch {
	case err != nil:
		writeError(w, code, err.Error())
	case missing:
		writeError(w, http.StatusNotFound, "no settings for user "+strconv.Quote(name))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) addExtra(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Seconds int64 `json:"seconds"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Seconds == 0 || body.Seconds < -86400 || body.Seconds > 86400 {
		writeError(w, http.StatusBadRequest, "seconds must be non-zero, between -86400 and 86400")
		return
	}
	if _, ok, err := s.user(name); err != nil || !ok {
		s.respondUser(w, name)
		return
	}
	s.mu.Lock()
	s.cfg.Tracker.AddExtra("user:"+name, time.Duration(body.Seconds)*time.Second, s.cfg.Now())
	s.saveTracker()
	s.mu.Unlock()
	s.respondUser(w, name)
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok, err := s.user(name); err != nil || !ok {
		s.respondUser(w, name)
		return
	}
	s.mu.Lock()
	s.cfg.Tracker.SetPaused("user:"+name, r.Method == http.MethodPut, s.cfg.Now())
	s.saveTracker()
	s.mu.Unlock()
	s.respondUser(w, name)
}

func (s *Server) saveTracker() {
	if err := s.cfg.Tracker.Save(); err != nil {
		log.Printf("api: saving allowance state: %v", err)
	}
}

// Device is a client seen on the network.
type Device struct {
	IP       string    `json:"ip"`
	MAC      string    `json:"mac,omitempty"`
	Label    string    `json:"client_label,omitempty"` // client software of its last flow
	JA4      string    `json:"ja4,omitempty"`
	Host     string    `json:"host,omitempty"` // host name of its last flow
	LastSeen time.Time `json:"last_seen"`
	User     string    `json:"user,omitempty"` // user it currently belongs to
	Rule     string    `json:"rule,omitempty"` // rule it currently matches
}

// Seen records a flow from a client, for GET /devices.
func (s *Server) Seen(ip netip.Addr, mac net.HardwareAddr, label, ja4, host string, t time.Time) {
	d := Device{IP: ip.Unmap().String(), Label: label, JA4: ja4, Host: host, LastSeen: t}
	key := d.IP
	if mac != nil {
		d.MAC = mac.String()
		key = d.MAC
	}
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if prev, ok := s.seen[key]; ok && d.JA4 == "" {
		// Keep what we know from TLS/QUIC flows over flows without a JA4.
		d.Label, d.JA4 = prev.Label, prev.JA4
	}
	if _, ok := s.seen[key]; !ok && len(s.seen) >= maxSeen {
		var oldest string
		for k, v := range s.seen {
			if oldest == "" || v.LastSeen.Before(s.seen[oldest].LastSeen) {
				oldest = k
			}
		}
		delete(s.seen, oldest)
	}
	s.seen[key] = &d
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	p := s.cfg.Policy()
	s.seenMu.Lock()
	ds := make([]Device, 0, len(s.seen))
	for _, d := range s.seen {
		ds = append(ds, *d)
	}
	s.seenMu.Unlock()
	for i := range ds {
		ip, _ := netip.ParseAddr(ds[i].IP)
		mac, _ := net.ParseMAC(ds[i].MAC)
		if m, ok := p.Match(ip, mac, ds[i].JA4); ok {
			ds[i].User, ds[i].Rule = m.User, m.Rule
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].LastSeen.After(ds[j].LastSeen) })
	writeJSON(w, http.StatusOK, ds)
}

// blocks returns recent block log entries, oldest first.
func (s *Server) blocks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since: want an RFC 3339 time")
			return
		}
		since = t
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxBlocks {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit: want 1 to %d", maxBlocks))
			return
		}
		limit = n
	}
	user := q.Get("user")

	es := []blocklog.Entry{}
	if s.cfg.BlockLog != "" {
		var err error
		if es, err = readBlocks(s.cfg.BlockLog, since, user, limit); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, es)
}

// readBlocks returns the last limit entries of the block log after since,
// reading at most its last blockLogTail bytes.
func readBlocks(path string, since time.Time, user string, limit int) ([]blocklog.Entry, error) {
	es := []blocklog.Entry{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return es, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := max(fi.Size()-blockLogTail, 0)
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if off > 0 {
		// Skip the partial first line.
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(buf))
	sc.Buffer(nil, 64<<10)
	for sc.Scan() {
		var e blocklog.Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if !e.Time.After(since) || (user != "" && e.User != user) {
			continue
		}
		es = append(es, e)
	}
	if len(es) > limit {
		es = es[len(es)-limit:]
	}
	return es, nil
}
