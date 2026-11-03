package pipeline

import "testing"

// StoredDKIMCovers counts a signature only when its stored verdict passed, its d= matches
// the verdict, and its h= names every field asked for.
func TestStoredDKIMCovers(t *testing.T) {
	const listHeaders = "From: news@news.example\r\n" +
		"List-Unsubscribe: <https://news.example/u>\r\n" +
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"
	sig := func(d, h string) string {
		return "DKIM-Signature: v=1; a=rsa-sha256; d=" + d + "; s=s1;\r\n\th=" + h + "; bh=x; b=y\r\n"
	}
	ar := func(results ...string) string {
		out := "Authentication-Results: restmail"
		for _, r := range results {
			out += ";\r\n\t" + r
		}
		return out + "\r\n"
	}
	covers := "From:List-Unsubscribe:\r\n\tlist-unsubscribe-post"
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"signed and covered", ar("dkim=pass header.d=news.example") + sig("news.example", covers) + listHeaders, true},
		{"unsigned", ar("dkim=none") + listHeaders, false},
		{"Post not covered", ar("dkim=pass header.d=news.example") + sig("news.example", "From:List-Unsubscribe") + listHeaders, false},
		{"failed", ar("dkim=fail header.d=news.example") + sig("news.example", covers) + listHeaders, false},
		{"no verdicts of this server", sig("news.example", covers) + listHeaders, false},
		{"another server's verdicts", "Authentication-Results: mx.other; dkim=pass header.d=news.example\r\n" + sig("news.example", covers) + listHeaders, false},
		{"covering signature failed, other passed",
			ar("dkim=pass header.d=news.example", "dkim=fail header.d=news.example") +
				sig("news.example", "From") + sig("news.example", covers) + listHeaders, false},
		{"second signature covers and passed",
			ar("dkim=fail header.d=other.example", "dkim=pass header.d=news.example") +
				sig("other.example", covers) + sig("news.example", covers) + listHeaders, true},
		{"verdict and field disagree on d=", ar("dkim=pass header.d=other.example") + sig("news.example", covers) + listHeaders, false},
		{"more fields than verdicts", ar("dkim=pass header.d=news.example") + sig("news.example", covers) + sig("news.example", covers) + listHeaders, false},
	}
	for _, c := range cases {
		if got := StoredDKIMCovers(c.header, "List-Unsubscribe", "List-Unsubscribe-Post"); got != c.want {
			t.Errorf("%s: StoredDKIMCovers = %v, want %v", c.name, got, c.want)
		}
	}
}
