package cli

import "strings"

func isLocalSlash(command string) bool {
	switch strings.ToLower(command) {
	case "/help", "/h", "/?", "/clear", "/new", "/status", "/sessions", "/model", "/budget", "/steps", "/save", "/load", "/list", "/delete", "/session", "/tools", "/plain", "/resume", "/select":
		return true
	default:
		return false
	}
}
