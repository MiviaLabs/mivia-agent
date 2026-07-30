package cli

import "strings"

func isLocalSlash(command string) bool {
	switch strings.ToLower(command) {
	case "/help", "/h", "/?", "/clear", "/status", "/sessions", "/model", "/budget", "/steps", "/save", "/load", "/list", "/delete", "/session", "/tools", "/plain", "/resume":
		return true
	default:
		return false
	}
}
