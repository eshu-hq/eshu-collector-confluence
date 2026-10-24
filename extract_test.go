// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractLinksFindsEveryLinkShapeInDocumentOrder(t *testing.T) {
	t.Parallel()

	body := `<p>Intro <a href="https://docs.example.invalid/a">Alpha   link</a></p>` +
		`<ac:link><ri:page ri:content-id="2002"/><ac:plain-text-link-body>By id</ac:plain-text-link-body></ac:link>` +
		`<ac:link><ri:page ri:content-title="Design Notes"/></ac:link>` +
		`<ac:link><ri:attachment ri:filename="diagram.png"/></ac:link>` +
		`<ac:link><ri:url ri:value="https://docs.example.invalid/b"/></ac:link>` +
		`<a>no href</a><a href="">empty</a><ac:link></ac:link>` +
		`<div><a href="confluence:page:3003">nested</a></div>`
	got := extractLinks(body)
	var hrefs []string
	for _, link := range got {
		hrefs = append(hrefs, link.href)
	}
	want := []string{
		"https://docs.example.invalid/a",
		"confluence:page:2002",
		"confluence:page-title:Design Notes",
		"confluence:attachment:diagram.png",
		"https://docs.example.invalid/b",
		"confluence:page:3003",
	}
	if !reflect.DeepEqual(hrefs, want) {
		t.Fatalf("hrefs = %v, want %v", hrefs, want)
	}
	if got[0].text != "Alpha link" {
		t.Errorf("anchor text = %q, want whitespace collapsed", got[0].text)
	}
	if got[1].text != "By id" {
		t.Errorf("storage link text = %q, want the link body", got[1].text)
	}
}

func TestPlainTextCollapsesWhitespaceAcrossNodes(t *testing.T) {
	t.Parallel()

	if got := plainText("<h1>Title</h1>\n<p>one   two</p><p>three</p>"); got != "Title one two three" {
		t.Errorf("plainText() = %q", got)
	}
	if got := plainText(""); got != "" {
		t.Errorf("plainText(empty) = %q, want empty", got)
	}
}

func TestTargetKindSeparatesExternalFromConfluenceReferences(t *testing.T) {
	t.Parallel()

	for href, want := range map[string]string{
		"https://docs.example.invalid/x": "external_uri",
		"http://docs.example.invalid/x":  "external_uri",
		"confluence:page:1":              "confluence_reference",
		"mailto:a@example.invalid":       "confluence_reference",
	} {
		if got := targetKind(href); got != want {
			t.Errorf("targetKind(%q) = %q, want %q", href, got, want)
		}
	}
}

func TestSafeSourceURI(t *testing.T) {
	t.Parallel()

	good := []string{"https://docs.example.invalid/x?y=1", "http://docs.example.invalid", " https://docs.example.invalid/x "}
	for _, uri := range good {
		if safeSourceURI(uri) == "" {
			t.Errorf("safeSourceURI(%q) refused a safe URI", uri)
		}
	}
	bad := []string{
		"", "   ", "/relative", "file:///etc/passwd", "javascript:alert(1)", "ftp://docs.example.invalid",
		"https://u:p@docs.example.invalid", "https://docs.example.invalid/?token=1", "https://docs.example.invalid/?X-Api-Key=1",
		"https://docs.example.invalid/?authorization=1", "https://docs.example.invalid/?password=1",
		"https://docs.example.invalid/\x00", "https://docs.example.invalid/a\nb", "https://", "https://%zz",
	}
	for _, uri := range bad {
		if got := safeSourceURI(uri); got != "" {
			t.Errorf("safeSourceURI(%q) = %q, want it refused", uri, got)
		}
	}
}

func TestCleanText(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"plain":           "plain",
		"":                "",
		"a\x00b":          "ab",
		"\x00\x00":        "",
		"bad\xffbyte":     "bad�byte",
		"tab\tand\x07bel": "tab\tand\x07bel",
		"ünïcode ☃":       "ünïcode ☃",
	}
	for in, want := range tests {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.Contains(cleanText(strings.Repeat("\x00x", 1000)), "\x00") {
		t.Error("cleanText left a NUL behind")
	}
}

func TestHashTextMatchesTheInTreePrefixedSHA256(t *testing.T) {
	t.Parallel()

	// sha256("") with the in-tree "sha256:" prefix.
	if got := hashText(""); got != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("hashText(empty) = %q", got)
	}
}
