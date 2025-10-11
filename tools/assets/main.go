package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type assetSpec struct {
	Source string
	Kind   string
}

var assets = []assetSpec{
	{Source: "css/wizard.css", Kind: "css"},
	{Source: "css/common.css", Kind: "css"},
	{Source: "css/admin.css", Kind: "css"},
	{Source: "css/queue.css", Kind: "css"},
	{Source: "js/common.js", Kind: "js"},
	{Source: "js/admin.js", Kind: "js"},
	{Source: "js/index.js", Kind: "js"},
	{Source: "js/queue.js", Kind: "js"},
}

const (
	staticDir = "static"
	distDir   = "static/dist"
)

func main() {
	if err := build(); err != nil {
		fmt.Fprintf(os.Stderr, "asset build failed: %v\n", err)
		os.Exit(1)
	}
}

func build() error {
	if err := os.RemoveAll(distDir); err != nil {
		return fmt.Errorf("remove dist: %w", err)
	}
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return fmt.Errorf("create dist: %w", err)
	}

	manifest := make(map[string]string, len(assets))

	for _, spec := range assets {
		srcPath := filepath.Join(staticDir, spec.Source)
		raw, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", spec.Source, err)
		}

		var minified []byte
		switch spec.Kind {
		case "css":
			minified, err = minifyCSS(raw)
		case "js":
			minified, err = minifyJS(raw)
		default:
			err = fmt.Errorf("unsupported asset kind %q", spec.Kind)
		}
		if err != nil {
			return fmt.Errorf("minify %s: %w", spec.Source, err)
		}

		hash := sha256.Sum256(minified)
		shortHash := hex.EncodeToString(hash[:])[:12]

		ext := filepath.Ext(spec.Source)
		base := strings.TrimSuffix(filepath.Base(spec.Source), ext)
		outDir := filepath.Join(distDir, filepath.Dir(spec.Source))
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("create asset dir: %w", err)
		}

		outName := fmt.Sprintf("%s-%s%s", base, shortHash, ext)
		outPath := filepath.Join(outDir, outName)
		if err := os.WriteFile(outPath, minified, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}

		key := filepath.ToSlash(spec.Source)
		rel := filepath.ToSlash(filepath.Join("dist", filepath.Dir(spec.Source), outName))
		manifest[key] = rel
		fmt.Printf("built %s -> %s\n", spec.Source, rel)
	}

	manifestPath := filepath.Join(distDir, "manifest.json")
	if err := writeManifest(manifestPath, manifest); err != nil {
		return err
	}
	return nil
}

func writeManifest(path string, manifest map[string]string) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	fmt.Printf("wrote manifest to %s\n", path)
	return nil
}

func minifyCSS(input []byte) ([]byte, error) {
	var out bytes.Buffer
	inComment := false
	inString := byte(0)
	escape := false

	for i := 0; i < len(input); i++ {
		c := input[i]

		if inComment {
			if c == '*' && i+1 < len(input) && input[i+1] == '/' {
				inComment = false
				i++
			}
			continue
		}
		if inString != 0 {
			out.WriteByte(c)
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == inString {
				inString = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			inString = c
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(input) && input[i+1] == '*' {
			inComment = true
			i++
			continue
		}
		if isCSSWhitespace(c) {
			continue
		}
		if isCSSPunctuation(c) {
			trimTrailingSpace(&out)
			out.WriteByte(c)
			continue
		}
		out.WriteByte(c)
	}
	return out.Bytes(), nil
}

func isCSSWhitespace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f'
}

func isCSSPunctuation(c byte) bool {
	switch c {
	case '{', '}', ':', ';', ',', '(', ')', '[', ']', '=', '+', '-', '*', '/', '!':
		return true
	}
	return false
}

func minifyJS(input []byte) ([]byte, error) {
	var out bytes.Buffer
	inLineComment := false
	inBlockComment := false
	inString := byte(0)
	inTemplate := false
	escape := false

	for i := 0; i < len(input); i++ {
		c := input[i]

		if inLineComment {
			if c == '\n' || c == '\r' {
				inLineComment = false
			} else {
				continue
			}
		}
		if inBlockComment {
			if c == '*' && i+1 < len(input) && input[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}

		if inString != 0 {
			out.WriteByte(c)
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == inString {
				inString = 0
			}
			continue
		}

		if inTemplate {
			out.WriteByte(c)
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '`' {
				inTemplate = false
			}
			continue
		}

		if c == '"' || c == '\'' {
			inString = c
			out.WriteByte(c)
			continue
		}
		if c == '`' {
			inTemplate = true
			out.WriteByte(c)
			continue
		}

		if c == '/' && i+1 < len(input) {
			next := input[i+1]
			if next == '/' {
				inLineComment = true
				i++
				continue
			}
			if next == '*' {
				inBlockComment = true
				i++
				continue
			}
		}

		if isJSWhitespace(c) {
			prev, prevOK := lastSignificantByte(&out)
			next, nextOK := nextNonWhitespace(input, i+1)
			if prevOK && nextOK && needsSpaceBetween(prev, next) {
				if out.Len() == 0 || out.Bytes()[out.Len()-1] != ' ' {
					out.WriteByte(' ')
				}
			}
			continue
		}

		if isJSPunctuation(c) {
			trimTrailingSpace(&out)
			out.WriteByte(c)
			continue
		}

		out.WriteByte(c)
	}

	return bytes.TrimSpace(out.Bytes()), nil
}

func isJSWhitespace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f'
}

func isJSPunctuation(c byte) bool {
	switch c {
	case '{', '}', '(', ')', '[', ']', '.', ',', ';', ':', '?', '+', '-', '*', '/', '%', '&', '|', '^', '!', '~', '<', '>', '=', '#':
		return true
	}
	return false
}

func trimTrailingSpace(buf *bytes.Buffer) {
	if buf.Len() == 0 {
		return
	}
	b := buf.Bytes()
	if isJSWhitespace(b[len(b)-1]) {
		buf.Truncate(buf.Len() - 1)
	}
}

func lastSignificantByte(buf *bytes.Buffer) (byte, bool) {
	b := buf.Bytes()
	for i := len(b) - 1; i >= 0; i-- {
		if !isJSWhitespace(b[i]) {
			return b[i], true
		}
	}
	return 0, false
}

func nextNonWhitespace(input []byte, start int) (byte, bool) {
	for i := start; i < len(input); i++ {
		if !isJSWhitespace(input[i]) {
			return input[i], true
		}
	}
	return 0, false
}

func needsSpaceBetween(prev, next byte) bool {
	return isIdentifierRune(prev) && isIdentifierRune(next)
}

func isIdentifierRune(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_' || b == '$'
}

// This ensures future additions fail fast when using unsupported directories.
func validateSources(base string, specs []assetSpec) error {
	for _, spec := range specs {
		if strings.Contains(spec.Source, "..") {
			return errors.New("asset source must not contain ..")
		}
		if _, err := os.Stat(filepath.Join(base, spec.Source)); err != nil {
			return fmt.Errorf("missing asset %s: %w", spec.Source, err)
		}
	}
	return nil
}

func init() {
	if err := validateSources(staticDir, assets); err != nil {
		panic(err)
	}
}
