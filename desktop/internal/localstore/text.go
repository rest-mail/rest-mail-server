package localstore

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/idna"
)

func HTMLText(source string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(source))
	var result strings.Builder
	ignored := 0
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(result.String()), " ")
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			if string(name) == "script" || string(name) == "style" {
				ignored++
			}
			result.WriteByte(' ')
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			if (string(name) == "script" || string(name) == "style") && ignored > 0 {
				ignored--
			}
			result.WriteByte(' ')
		case html.TextToken:
			if ignored == 0 {
				result.Write(tokenizer.Text())
				result.WriteByte(' ')
			}
		}
	}
}

func addresses(value string) string {
	var variants []string
	for _, token := range strings.FieldsFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || strings.ContainsRune(`"<>[](){},;`, character)
	}) {
		local, domain, found := strings.Cut(token, "@")
		if !found {
			continue
		}
		if ascii, err := idna.Lookup.ToASCII(domain); err == nil {
			variants = append(variants, local+"@"+ascii)
		}
		if decoded, err := idna.Lookup.ToUnicode(domain); err == nil {
			variants = append(variants, local+"@"+decoded)
		}
	}
	return value + " " + strings.Join(variants, " ")
}
