package panel

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

var adminName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{1,47}$`)
var hashEncoding = base64.RawStdEncoding

const hashPrefix = "$argon2id$v=19$m=65536,t=3,p=1$"
const cookieName = "backpack_session"

func passwordKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)
}

func HashPassword(password string) (string, error) {
	if len(password) < 14 || len(password) > 256 {
		return "", errors.New("password must contain 14–256 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return hashPrefix + hashEncoding.EncodeToString(salt) + "$" + hashEncoding.EncodeToString(passwordKey(password, salt)), nil
}

func parseHash(hash string) ([]byte, []byte, bool) {
	body, ok := strings.CutPrefix(hash, hashPrefix)
	saltPart, keyPart, split := strings.Cut(body, "$")
	salt, saltErr := hashEncoding.DecodeString(saltPart)
	key, keyErr := hashEncoding.DecodeString(keyPart)
	return salt, key, ok && split && saltErr == nil && keyErr == nil && len(salt) == 16 && len(key) == 32
}

func validHash(hash string) bool { _, _, ok := parseHash(hash); return ok }

func verify(hash, password string) bool {
	salt, key, ok := parseHash(hash)
	if !ok {
		salt, key = make([]byte, 16), make([]byte, 32)
	}
	match := subtle.ConstantTimeCompare(passwordKey(password, salt), key) == 1
	return ok && match
}

type session struct {
	name, csrf string
	expires    time.Time
}

type limit struct {
	count   int
	expires time.Time
}

type auth struct {
	mu       sync.Mutex
	sessions map[string]session
	attempts map[string]limit
	slots    chan struct{}
	duration time.Duration
	secure   bool
}

func token() string {
	value := make([]byte, 32)
	_, _ = rand.Read(value)
	return base64.RawURLEncoding.EncodeToString(value)
}

func (a *auth) allowed(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.attempts) > 1024 {
		for key, entry := range a.attempts {
			if now.After(entry.expires) {
				delete(a.attempts, key)
			}
		}
	}
	l, exists := a.attempts[host]
	if !exists && len(a.attempts) >= 4096 {
		return false
	}
	if now.After(l.expires) {
		l = limit{expires: now.Add(time.Minute)}
	}
	if l.count >= 8 {
		return false
	}
	l.count++
	a.attempts[host] = l
	return true
}

func (a *auth) start(w http.ResponseWriter, name string) (session, bool) {
	id := token()
	s := session{name: name, csrf: token(), expires: time.Now().Add(a.duration)}
	a.mu.Lock()
	for key, entry := range a.sessions {
		if time.Now().After(entry.expires) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 512 {
		a.mu.Unlock()
		return session{}, false
	}
	a.sessions[id] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, Secure: a.secure,
		SameSite: http.SameSiteStrictMode, Expires: s.expires})
	return s, true
}

func (a *auth) lookup(r *http.Request) (session, string, bool) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[cookie.Value]
	if !ok || time.Now().After(s.expires) {
		delete(a.sessions, cookie.Value)
		return session{}, "", false
	}
	return s, cookie.Value, true
}

func (a *auth) revoke(id, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id != "" {
		delete(a.sessions, id)
	}
	if name != "" {
		for key, value := range a.sessions {
			if value.name == name {
				delete(a.sessions, key)
			}
		}
	}
}
