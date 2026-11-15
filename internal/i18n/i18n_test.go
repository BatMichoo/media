package i18n

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSameKeys(t *testing.T) {
	for k := range texts["en"] {
		assert.NotEmpty(t, texts["bg"][k], "bg missing %s", k)
	}
	for k := range texts["bg"] {
		assert.NotEmpty(t, texts["en"][k], "en missing %s", k)
	}
}

func TestFallbacks(t *testing.T) {
	assert.Equal(t, "Continue watching", T("de", "home.continue"))
	assert.Equal(t, "Продължи гледането", T("bg", "home.continue"))
	assert.Equal(t, "nope", T("en", "nope"))
	assert.Equal(t, "en", Lang(""))
}
