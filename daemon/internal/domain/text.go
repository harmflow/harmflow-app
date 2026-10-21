package domain

import (
	"errors"
	"strings"
)

// Text is a text the daemon writes for the user. The daemon writes en-US only: Key and Params let the desktop show it
// in the interface's language, and Message is the en-US text, kept as the fallback (FS-012 FR-005). The agent's text,
// tool output, and a tool's own error get no key; a Text that quotes one carries it in a param, as it is.
type Text struct {
	Key     string            `json:"messageKey,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
	Message string            `json:"message"`
}

// T builds a Text from its key, one of MessageKey in contracts/api/openapi.yaml, and its en-US template, whose
// {{name}} placeholders take the params, given as name, value pairs.
func T(key, template string, params ...string) Text {
	t := Text{Key: key, Message: template}
	if len(params) < 2 {
		return t
	}
	t.Params = make(map[string]string, len(params)/2)
	pairs := make([]string, 0, len(params))
	for i := 0; i+1 < len(params); i += 2 {
		t.Params[params[i]] = params[i+1]
		pairs = append(pairs, "{{"+params[i]+"}}", params[i+1])
	}
	t.Message = strings.NewReplacer(pairs...).Replace(template)
	return t
}

// TextError is an error whose text the desktop can translate.
type TextError struct{ Text }

func (e *TextError) Error() string { return e.Message }

// TextErr is the error of T(key, template, params...).
func TextErr(key, template string, params ...string) error {
	return &TextError{T(key, template, params...)}
}

// TextOf is the Text an error carries; any other error becomes a Text with no key, its message as it is.
func TextOf(err error) Text {
	var te *TextError
	if errors.As(err, &te) {
		return te.Text
	}
	return Text{Message: err.Error()}
}
