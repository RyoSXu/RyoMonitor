//go:build trustproxy

package main

import (
	"fmt"
	"net/http"
)

func loadAuthConfig() {}

func authenticated(string) bool { return true }

func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleLogin(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method == http.MethodPost {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func redirectUnauthenticated(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func writeGenEnvAuth(string) {
	fmt.Println("# Built with -tags trustproxy: no built-in auth; trust upstream SSO/reverse proxy.")
}
