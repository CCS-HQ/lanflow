package applog

import (
	"regexp"
	"strings"
)

// Redact is the last gate before lanflow.log, errors.jsonl (which travels
// with feedback), audit.log and the diagnostics bundle. It is applied to
// free text, so it works by shape:
//
//   - proxy share links (ss://, vmess://, trojan://, …) are masked whole:
//     every part of them is a credential or the node's address;
//   - other URLs keep their scheme and host, for troubleshooting, and lose
//     the user info, path and query (subscription links carry tokens there);
//   - Authorization and Cookie headers, Bearer and Basic credentials;
//   - values of credential-like keys, in key=value, key: value, YAML and
//     JSON ("key":"value", also escaped inside a string) form.
var (
	shareRe = regexp.MustCompile(`(?i)\b(ssr?|vmess|vless|trojan(?:-go)?|hysteria2?|hy2|tuic|snell|wireguard|wg|anytls|juicity|mieru|brook|naive\+https?)://[^\s"'<>\\]+`)
	urlRe   = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([^/\s"'<>?#\\]+)([^\s"'<>\\]*)`)
	// sealedRe: secrets sealed with the sync passphrase (lanflow.yaml)
	sealedRe = regexp.MustCompile(`\benc:v1:[A-Za-z0-9+/=]+`)
	// authzRe: an Authorization header or field; the scheme word is kept.
	authzRe = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization)(\\?["']?\s*[:=]\s*\\?["']?)((?:bearer|basic|digest|token|negotiate)\s+)?([^\s"',;\\]+)`)
	// bearerRe, basicRe: the credentials on their own (a curl line, a
	// header dump).
	bearerRe = regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	basicRe  = regexp.MustCompile(`(?i)\b(basic\s+)([A-Za-z0-9+/]{8,}={0,2})`)
	cookieRe = regexp.MustCompile(`(?i)\b((?:set-)?cookie)(\\?["']?\s*[:=]\s*\\?["']?)[^\r\n"'\\]+`)
	// secretRe: key, separator, value. The key may be quoted (JSON) and
	// may carry a prefix (access_token, private-key); "key" alone or after
	// "_" / "-" only, so that words like hotkey are left alone. The value
	// is a quoted string, masked whole, or runs to the next separator.
	secretRe = regexp.MustCompile(`(?i)((?:\\?["'])?\b(?:[\w-]*?(?:token|secret|password|passwd|passphrase)|pass|auth(?:[_-]str)?|(?:[\w-]*[_-])?key|uuid|psk|sid)(?:\\?["'])?\s*[=:]\s*)(\\"[^"\\]*\\"|"(?:[^"\\]|\\.)*"|'[^']*'|[^\s&"',;{\[\\][^\s&"',;]*)`)
)

// Redact masks what must not end up in a log or a bug report.
func Redact(s string) string {
	// Most strings (names, counts, plain messages) have none of the
	// shapes: each rule runs only where its marker occurs.
	if strings.Contains(s, "://") {
		s = shareRe.ReplaceAllString(s, "$1://***")
		s = urlRe.ReplaceAllStringFunc(s, func(m string) string {
			sub := urlRe.FindStringSubmatch(m)
			return redactURL(sub[1], sub[2], sub[3])
		})
	}
	if hasFold(s, "bearer") {
		s = bearerRe.ReplaceAllString(s, "$1***")
	}
	if hasFold(s, "basic") {
		s = basicRe.ReplaceAllStringFunc(s, func(m string) string {
			sub := basicRe.FindStringSubmatch(m)
			if !looksEncoded(sub[2]) {
				return m // "basic settings": a word, not base64
			}
			return sub[1] + "***"
		})
	}
	if !strings.ContainsAny(s, ":=") {
		return s
	}
	s = sealedRe.ReplaceAllString(s, "enc:v1:***")
	s = authzRe.ReplaceAllString(s, "$1$2$3***")
	s = cookieRe.ReplaceAllString(s, "$1$2***")
	return secretRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretRe.FindStringSubmatch(m)
		switch v := sub[2]; {
		case strings.HasPrefix(v, `\"`):
			return sub[1] + `\"***\"`
		case strings.HasPrefix(v, `"`):
			return sub[1] + `"***"`
		case strings.HasPrefix(v, `'`):
			return sub[1] + `'***'`
		}
		// A closing bracket after a bare value belongs to what is around
		// it: {"sid":123}.
		v := sub[2]
		return sub[1] + "***" + v[len(strings.TrimRight(v, "}])")):]
	})
}

// hasFold reports whether s contains word (lower case ASCII) in any case.
func hasFold(s, word string) bool {
	for i := 0; i+len(word) <= len(s); i++ {
		if c := s[i] | 0x20; c == word[0] && strings.EqualFold(s[i:i+len(word)], word) {
			return true
		}
	}
	return false
}

// redactURL keeps the scheme and the host of a URL.
func redactURL(scheme, auth, rest string) string {
	if i := strings.LastIndex(auth, "@"); i >= 0 { // user:pass@host
		auth = "***@" + auth[i+1:]
	} else if i := strings.IndexByte(auth, ':'); i >= 0 && !strings.HasPrefix(auth, "[") && !isDigits(auth[i+1:]) {
		// user:pa/ss@host: the "/" of the password ended what looked
		// like the host.
		at := strings.LastIndex(rest, "@")
		if at < 0 {
			return scheme + "***"
		}
		host := rest[at+1:]
		rest = ""
		if j := strings.IndexAny(host, "/?#"); j >= 0 {
			host, rest = host[:j], host[j:]
		}
		auth = "***@" + host
	}
	if rest == "" || rest == "/" {
		return scheme + auth + rest
	}
	return scheme + auth + "/***"
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// looksEncoded tells base64 from an English word after "Basic": a digit,
// padding, "+", "/" or a capital inside the word.
func looksEncoded(s string) bool {
	for i, c := range s {
		switch {
		case c >= '0' && c <= '9', c == '=', c == '+', c == '/':
			return true
		case i > 0 && c >= 'A' && c <= 'Z':
			return true
		}
	}
	return false
}
