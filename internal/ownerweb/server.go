package ownerweb

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"golang.org/x/crypto/bcrypt"
)

//go:embed static
var assets embed.FS

const sessionTTL = 12 * time.Hour
const reviewTTL = 5 * time.Minute
const bodyLimit = 20 << 10

type reviewGrant struct {
	id, decision, body string
	expires            time.Time
}
type session struct {
	csrf    string
	expires time.Time
	reviews map[string]reviewGrant
}

type Server struct {
	cfg                    Config
	origin, prefix, cookie string
	backend                Backend
	ledger                 *Ledger
	mu                     sync.Mutex
	sessions               map[string]*session
	attempts               []time.Time
	loginSlots             chan struct{}
}

func New(c Config, backend Backend, ledger *Ledger) (*Server, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if backend == nil || ledger == nil {
		return nil, errors.New("owner web needs its scoped relay backend and submission ledger")
	}
	origin, prefix := c.location()
	cookie := "__Host-tincan-owner"
	if prefix != "/" {
		cookie = "__Secure-tincan-owner"
	}
	if c.Dev {
		cookie = "tincan-owner-dev"
	}
	return &Server{cfg: c, origin: origin, prefix: prefix, cookie: cookie, backend: backend, ledger: ledger, sessions: map[string]*session{}, loginSlots: make(chan struct{}, 2)}, nil
}

func token() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func output(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, code int, message string) {
	output(w, code, map[string]string{"error": message})
}

func readJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyLimit))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		problem(w, 400, "Invalid request.")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		problem(w, 400, "Send one JSON object.")
		return false
	}
	return true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if !s.cfg.Dev {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	u, _ := url.Parse(s.origin)
	if !strings.EqualFold(r.Host, u.Host) {
		problem(w, 403, "Invalid host.")
		return
	}
	if s.prefix != "/" && r.URL.Path == strings.TrimSuffix(s.prefix, "/") && r.Method == "GET" {
		http.Redirect(w, r, s.prefix, 302)
		return
	}
	if !strings.HasPrefix(r.URL.Path, s.prefix) {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, s.prefix)
	if r.Method != "GET" && r.Method != "POST" {
		problem(w, 405, "Method not allowed.")
		return
	}
	if r.Method == "POST" {
		contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		fetchSite := r.Header.Get("Sec-Fetch-Site")
		if r.Header.Get("Origin") != s.origin || contentType != "application/json" || r.Header.Get("X-Tincan-UI") != "1" || (fetchSite != "" && fetchSite != "same-origin") {
			problem(w, 403, "Use this app's request controls.")
			return
		}
	}
	if path == "api/login" && r.Method == "POST" {
		s.login(w, r)
		return
	}
	if !strings.HasPrefix(path, "api/") {
		if r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		if path == "" {
			path = "index.html"
		}
		if path != "index.html" && path != "app.js" && path != "style.css" {
			http.NotFound(w, r)
			return
		}
		body, err := fs.ReadFile(assets, "static/"+path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctype := "text/html; charset=utf-8"
		if path == "app.js" {
			ctype = "text/javascript; charset=utf-8"
		}
		if path == "style.css" {
			ctype = "text/css; charset=utf-8"
		}
		h.Set("Content-Type", ctype)
		w.Write(body)
		return
	}
	key, sess := s.authenticate(r)
	if sess == nil {
		problem(w, 401, "Sign in to continue.")
		return
	}
	if r.Method == "POST" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.csrf)) != 1 {
		problem(w, 403, "Reload the page before trying again.")
		return
	}
	if path == "api/session" && r.Method == "GET" {
		output(w, 200, map[string]any{"csrf": sess.csrf, "targets": s.cfg.Targets, "expires_at": sess.expires})
		return
	}
	if path == "api/logout" && r.Method == "POST" {
		s.mu.Lock()
		delete(s.sessions, key)
		s.mu.Unlock()
		s.setCookie(w, "", -1)
		output(w, 200, map[string]bool{"signed_out": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	switch {
	case path == "api/targets" && r.Method == "GET":
		targets, err := s.backend.Agents(ctx)
		if err != nil {
			s.backendError(w, err)
			return
		}
		output(w, 200, map[string]any{"targets": targets})
	case path == "api/requests" && r.Method == "GET":
		items, err := s.backend.List(ctx)
		if err != nil {
			s.backendError(w, err)
			return
		}
		output(w, 200, map[string]any{"requests": items})
	case path == "api/requests" && r.Method == "POST":
		s.send(w, r, ctx)
	case strings.HasPrefix(path, "api/requests/"):
		parts := strings.Split(strings.TrimPrefix(path, "api/requests/"), "/")
		if len(parts) > 2 || !requestID.MatchString(parts[0]) {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		if len(parts) == 1 && r.Method == "GET" {
			item, err := s.backend.Get(ctx, id)
			if err != nil {
				s.backendError(w, err)
				return
			}
			output(w, 200, item)
			return
		}
		if len(parts) == 2 && r.Method == "POST" && (parts[1] == "review" || parts[1] == "decision") {
			s.review(w, r, ctx, key, id, parts[1])
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

var requestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var submissionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{20,80}$`)

func (s *Server) authenticate(r *http.Request) (string, *session) {
	cookie, err := r.Cookie(s.cookie)
	if err != nil || len(cookie.Value) > 100 {
		return "", nil
	}
	key := digest(cookie.Value)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[key]
	if sess == nil {
		return "", nil
	}
	if !time.Now().Before(sess.expires) {
		delete(s.sessions, key)
		return "", nil
	}
	return key, sess
}

func (s *Server) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: value, Path: s.prefix, Secure: !s.cfg.Dev, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.Lock()
	now := time.Now()
	attempts := s.attempts[:0]
	for _, at := range s.attempts {
		if now.Sub(at) < time.Minute {
			attempts = append(attempts, at)
		}
	}
	s.attempts = attempts
	blocked := len(attempts) >= 5
	if !blocked {
		s.attempts = append(s.attempts, now)
	}
	s.mu.Unlock()
	if blocked {
		w.Header().Set("Retry-After", "60")
		problem(w, 429, "Too many sign-in attempts. Try again in a minute.")
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		problem(w, 429, "Sign-in is busy. Try again shortly.")
		return
	}
	if len(in.Password) > 72 || bcrypt.CompareHashAndPassword([]byte(s.cfg.PasswordHash), []byte(in.Password)) != nil {
		problem(w, 401, "That passphrase did not match.")
		return
	}
	s.mu.Lock()
	for key, sess := range s.sessions {
		if !now.Before(sess.expires) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 10 {
		s.mu.Unlock()
		problem(w, 429, "Too many active sessions. Sign out of another device or restart the service.")
		return
	}
	// A successful sign-in replaces this browser's old session.
	if old, err := r.Cookie(s.cookie); err == nil {
		delete(s.sessions, digest(old.Value))
	}
	raw := token()
	sess := &session{csrf: token(), expires: now.Add(sessionTTL), reviews: map[string]reviewGrant{}}
	s.sessions[digest(raw)] = sess
	s.mu.Unlock()
	s.setCookie(w, raw, int(sessionTTL.Seconds()))
	output(w, 200, map[string]any{"csrf": sess.csrf, "expires_at": sess.expires})
}

func (s *Server) send(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	var in struct {
		Target string `json:"target"`
		Body   string `json:"body"`
		Key    string `json:"submission_key"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	allowed := false
	for _, target := range s.cfg.Targets {
		if target == in.Target {
			allowed = true
		}
	}
	in.Body = strings.TrimSpace(in.Body)
	if !allowed || in.Body == "" || len(in.Body) > 16384 || !submissionID.MatchString(in.Key) {
		problem(w, 400, "Choose a configured teammate and write a request up to 16 KiB.")
		return
	}
	key := s.cfg.Agent + ":" + in.Key
	fresh, result, err := s.ledger.claim(ctx, key, in.Target, in.Body)
	if err != nil {
		problem(w, 409, "This submission is already pending or uncertain. Check your history before starting another.")
		return
	}
	if !fresh {
		output(w, 200, result)
		return
	}
	result, sendErr := s.backend.Send(ctx, in.Target, in.Body)
	// Persist the outcome even if the browser disconnected. An interrupted
	// pending row remains blocked across a restart rather than being resent.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.ledger.finish(saveCtx, key, result, sendErr); err != nil || sendErr != nil {
		output(w, 502, map[string]any{"error": "The relay outcome is uncertain. Check your request history before sending again.", "uncertain": true})
		return
	}
	output(w, 201, result)
}

func (s *Server) review(w http.ResponseWriter, r *http.Request, ctx context.Context, key, id, action string) {
	item, err := s.backend.Get(ctx, id)
	if err != nil {
		s.backendError(w, err)
		return
	}
	if item.Status != envelope.StatusHeld {
		problem(w, 409, "This request is no longer waiting for approval. Refresh its status.")
		return
	}
	if len(item.Request.Attachments) > 0 || item.Request.Then != nil || item.Request.FollowOf != "" {
		problem(w, 409, "Review this request using the relay's existing admin tools.")
		return
	}
	if action == "review" {
		var empty struct{}
		if !readJSON(w, r, &empty) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		sess := s.sessions[key]
		if sess == nil || !time.Now().Before(sess.expires) {
			problem(w, 401, "Sign in to continue.")
			return
		}
		for grant, v := range sess.reviews {
			if !time.Now().Before(v.expires) || v.id == id {
				delete(sess.reviews, grant)
			}
		}
		if len(sess.reviews) > 20 {
			problem(w, 429, "Close another review before opening this one.")
			return
		}
		approve, deny := token(), token()
		for decision, raw := range map[string]string{"approve": approve, "deny": deny} {
			sess.reviews[digest(raw)] = reviewGrant{id: id, decision: decision, body: digest(item.Request.Body), expires: time.Now().Add(reviewTTL)}
		}
		output(w, 200, map[string]any{"request": item.Request, "approve_token": approve, "deny_token": deny})
		return
	}
	var in struct {
		Decision string `json:"decision"`
		Token    string `json:"review_token"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.Lock()
	sess := s.sessions[key]
	var grant reviewGrant
	var found bool
	if sess != nil {
		grant, found = sess.reviews[digest(in.Token)]
		delete(sess.reviews, digest(in.Token))
	}
	valid := sess != nil && time.Now().Before(sess.expires) && found && grant.id == id && grant.decision == in.Decision && grant.body == digest(item.Request.Body) && time.Now().Before(grant.expires)
	if valid {
		for k, v := range sess.reviews {
			if v.id == id {
				delete(sess.reviews, k)
			}
		}
	}
	s.mu.Unlock()
	if !valid {
		problem(w, 409, "This review expired or changed. Open a fresh review.")
		return
	}
	if err := s.backend.Decide(ctx, id, in.Decision); err != nil {
		s.backendError(w, err)
		return
	}
	output(w, 200, map[string]string{"decision": in.Decision})
}

func (s *Server) backendError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		problem(w, 404, "Request not found.")
		return
	}
	if errors.Is(err, ErrNotHeld) {
		problem(w, 409, "This request was already decided. Refresh its status.")
		return
	}
	problem(w, 502, "The relay is unavailable. Your saved requests are preserved; refresh to reconnect.")
}
