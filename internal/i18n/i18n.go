// Package i18n holds the media app's texts in English and Bulgarian.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed *.json
var files embed.FS

// CookieName is the language cookie every home server app shares.
const CookieName = "homeserver_lang"

var texts = func() map[string]map[string]string {
	all := map[string]map[string]string{}
	for _, lang := range []string{"en", "bg"} {
		b, err := files.ReadFile(lang + ".json")
		if err != nil {
			panic(fmt.Sprintf("i18n: %v", err))
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			panic(fmt.Sprintf("i18n: %s: %v", lang, err))
		}
		all[lang] = m
	}
	return all
}()

// Lang is lang if supported, else "en".
func Lang(lang string) string {
	if _, ok := texts[lang]; ok {
		return lang
	}
	return "en"
}

// T is the text for key in lang (English if missing, the key if unknown).
func T(lang, key string) string {
	if s, ok := texts[Lang(lang)][key]; ok {
		return s
	}
	if s, ok := texts["en"][key]; ok {
		return s
	}
	return key
}
