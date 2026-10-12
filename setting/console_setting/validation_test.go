package console_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRechargeStoreURL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "default store", input: "https://catfk.com/shop/40PFAHE3", want: "https://catfk.com/shop/40PFAHE3"},
		{name: "trim surrounding whitespace", input: " \thttps://catfk.com/shop/40PFAHE3\n", want: "https://catfk.com/shop/40PFAHE3"},
		{name: "HTTP local preview", input: "http://localhost:8080/shop", want: "http://localhost:8080/shop"},
		{name: "IPv6 and store parameters", input: "https://[::1]:8443/shop?category=cards#products", want: "https://[::1]:8443/shop?category=cards#products"},
		{name: "empty", input: "   "},
		{name: "relative URL", input: "/shop"},
		{name: "missing host", input: "https://"},
		{name: "JavaScript", input: "javascript:alert(1)"},
		{name: "data", input: "data:text/html,shop"},
		{name: "credentials", input: "https://user:password@catfk.com/shop"},
		{name: "empty userinfo", input: "https://@catfk.com/shop"},
		{name: "backslashes", input: "https:\\\\catfk.com\\shop"},
		{name: "embedded whitespace", input: "https://catfk.com/shop name"},
		{name: "invalid port", input: "https://catfk.com:65536/shop"},
		{name: "invalid escape", input: "https://catfk.com/%zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRechargeStoreURL(tc.input)
			if tc.want == "" {
				require.Error(t, err)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
