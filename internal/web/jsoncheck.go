package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/feernandooff/mockstro/internal/engine"
)

// checkBodyMatchesContentType makes a JSON response valid JSON and fills in the Content-Type
// when the tester left it out, so the app never receives a half-typed body.
func checkBodyMatchesContentType(c *engine.CustomResponse) error {
	var contentType string
	for k, v := range c.Headers {
		if strings.EqualFold(k, "Content-Type") {
			contentType = v
		}
	}
	if strings.TrimSpace(c.Body) == "" {
		return nil
	}
	valid := json.Valid([]byte(c.Body))
	switch {
	case contentType == "" && valid:
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		c.Headers["Content-Type"] = "application/json"
	case strings.Contains(strings.ToLower(contentType), "json") && !valid:
		return jsonError(c.Body)
	}
	return nil
}

// jsonError names the line and column of the first syntax error.
func jsonError(body string) error {
	var v any
	err := json.Unmarshal([]byte(body), &v)
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		line, col := 1, 1
		for _, r := range body[:min(int(syn.Offset), len(body))] {
			if r == '\n' {
				line, col = line+1, 1
			} else {
				col++
			}
		}
		return fmt.Errorf("body is not valid JSON: %s (line %d, column %d)", syn.Error(), line, col)
	}
	return fmt.Errorf("body is not valid JSON: %w", err)
}
