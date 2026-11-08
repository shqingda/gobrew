package platform

import (
	"fmt"
	"strings"
)

// FormComponent is Ruby's URI.encode_www_form_component, which brew uses for
// search queries: letters, digits and *-._ stay, a space becomes +, and every
// other byte is percent-encoded.
func FormComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', strings.IndexByte("*-._", c) >= 0:
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
