//go:build !trustproxy

package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "embed"
)

var b64 = base64.RawURLEncoding

//go:embed login.html
var loginPage string

var (
	cookieName   string
	sessionTTL   int64
	passwordHash string
	secret       []byte
	trustProxy   bool
)

func loadAuthConfig() {
	cookieName = env("MON_AUTH_COOKIE", "ryo_mon_session")
	ttl, err := strconv.ParseInt(env("MON_AUTH_SESSION_TTL", strconv.Itoa(7*24*60*60)), 10, 64)
	if err != nil {
		ttl = 7 * 24 * 60 * 60
	}
	sessionTTL = ttl
	trustProxy = os.Getenv("MON_AUTH_TRUST_PROXY") == "1"
	if trustProxy {
		passwordHash = os.Getenv("MON_AUTH_PASSWORD_HASH")
		secret = []byte(os.Getenv("MON_AUTH_SECRET"))
	} else {
		passwordHash = mustEnv("MON_AUTH_PASSWORD_HASH")
		secret = []byte(mustEnv("MON_AUTH_SECRET"))
	}
}

func authenticated(cookieHeader string) bool {
	if trustProxy {
		return true
	}
	return validSession(cookieHeader)
}

func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha256.Size
	numBlocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, numBlocks*hashLen)
	for block := 1; block <= numBlocks; block++ {
		prf := hmac.New(sha256.New, password)
		prf.Write(salt)
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], uint32(block))
		prf.Write(idx[:])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

func verifyPassword(password string) bool {
	parts := strings.SplitN(passwordHash, "$", 4)
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	salt, err := b64.DecodeString(parts[2])
	if err != nil {
		return false
	}
	derived := pbkdf2SHA256([]byte(password), salt, iter, sha256.Size)
	return subtle.ConstantTimeCompare([]byte(b64.EncodeToString(derived)), []byte(parts[3])) == 1
}

func signPayload(payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return b64.EncodeToString([]byte(payload)) + "." + b64.EncodeToString(mac.Sum(nil))
}

func validSession(cookieHeader string) bool {
	if cookieHeader == "" {
		return false
	}
	var token string
	for _, part := range strings.Split(cookieHeader, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] == cookieName {
			token = kv[1]
		}
	}
	if token == "" || !strings.Contains(token, ".") {
		return false
	}
	dot := strings.LastIndex(token, ".")
	payloadB64, signature := token[:dot], token[dot+1:]
	payloadBytes, err := b64.DecodeString(payloadB64)
	if err != nil {
		return false
	}
	payload := string(payloadBytes)
	expected := signPayload(payload)
	expectedSig := expected[strings.LastIndex(expected, ".")+1:]
	if subtle.ConstantTimeCompare([]byte(signature), []byte(expectedSig)) != 1 {
		return false
	}
	colon := strings.Index(payload, ":")
	if colon < 0 {
		return false
	}
	expiresAt, err := strconv.ParseInt(payload[:colon], 10, 64)
	if err != nil {
		return false
	}
	return expiresAt >= time.Now().Unix()
}

func makeSession() string {
	nonceBytes := make([]byte, 18)
	rand.Read(nonceBytes)
	payload := fmt.Sprintf("%d:%s", time.Now().Unix()+sessionTTL, b64.EncodeToString(nonceBytes))
	return signPayload(payload)
}

func sendLogin(w http.ResponseWriter, r *http.Request, statusCode int, errorKey, nextPath string) {
	page := strings.ReplaceAll(loginPage, "__ERROR_KEY__", htmlEscapeAttr(errorKey))
	page = strings.ReplaceAll(page, "__NEXT__", htmlEscapeAttr(nextPath))
	body := []byte(page)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

func htmlEscapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")
	return r.Replace(s)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: 0, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func handleLogin(w http.ResponseWriter, r *http.Request, cookie string) {
	if r.Method == http.MethodPost {
		handleLoginPost(w, r)
		return
	}
	if authenticated(cookie) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := r.URL.Query().Get("next")
	if !strings.HasPrefix(next, "/") {
		next = "/"
	}
	sendLogin(w, r, http.StatusOK, "", next)
}

func handleLoginPost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	password := r.PostFormValue("password")
	next := r.PostFormValue("next")
	if !strings.HasPrefix(next, "/") {
		next = "/"
	}
	if !verifyPassword(password) {
		time.Sleep(800 * time.Millisecond)
		sendLogin(w, r, http.StatusUnauthorized, "invalidPassword", next)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: makeSession(), Path: "/",
		MaxAge: int(sessionTTL), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func redirectUnauthenticated(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
}

func writeGenEnvAuth(password string) {
	salt := make([]byte, 16)
	rand.Read(salt)
	const iter = 260000
	dk := pbkdf2SHA256([]byte(password), salt, iter, sha256.Size)
	sec := make([]byte, 36)
	rand.Read(sec)
	fmt.Println("MON_AUTH_SESSION_TTL=604800")
	fmt.Printf("MON_AUTH_PASSWORD_HASH=pbkdf2_sha256$%d$%s$%s\n", iter, b64.EncodeToString(salt), b64.EncodeToString(dk))
	fmt.Println("MON_AUTH_SECRET=" + b64.EncodeToString(sec))
}
