// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package confluence

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// extractedLink is one outbound link found in a page body.
type extractedLink struct {
	href string
	text string
}

// extractLinks returns the hyperlinks and Confluence storage links in body, in
// document order. A body the HTML parser rejects yields no links. The walk is
// iterative so a hostile, deeply nested body cannot exhaust the stack.
func extractLinks(body string) []extractedLink {
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	var links []extractedLink
	walkPreorder(root, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}
		switch node.Data {
		case "a":
			if href := attr(node, "href"); href != "" {
				links = append(links, extractedLink{href: href, text: nodeText(node)})
			}
		case "ac:link":
			if href := confluenceStorageHref(node); href != "" {
				links = append(links, extractedLink{href: href, text: nodeText(node)})
			}
		}
	})
	return links
}

func confluenceStorageHref(link *html.Node) string {
	resource := firstResourceNode(link)
	if resource == nil {
		return ""
	}
	switch resource.Data {
	case "ri:page":
		if id := attr(resource, "ri:content-id"); id != "" {
			return "confluence:page:" + id
		}
		if title := attr(resource, "ri:content-title"); title != "" {
			return "confluence:page-title:" + title
		}
	case "ri:attachment":
		if filename := attr(resource, "ri:filename"); filename != "" {
			return "confluence:attachment:" + filename
		}
	case "ri:url":
		return attr(resource, "ri:value")
	}
	return ""
}

func firstResourceNode(node *html.Node) *html.Node {
	var resource *html.Node
	walkSubtree(node, func(current *html.Node) bool {
		if current.Type == html.ElementNode && strings.HasPrefix(current.Data, "ri:") {
			resource = current
			return false
		}
		return true
	})
	return resource
}

// plainText returns the visible text of body with whitespace collapsed. A body
// the parser rejects is returned unchanged.
func plainText(body string) string {
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return body
	}
	return strings.TrimSpace(nodeText(root))
}

// nodeText concatenates the text nodes under node in document order, collapsing
// whitespace. Each text node is followed by a space before the collapse, which
// matches the in-tree collector's output.
func nodeText(node *html.Node) string {
	var builder strings.Builder
	walkSubtree(node, func(current *html.Node) bool {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		return true
	})
	return strings.Join(strings.Fields(builder.String()), " ")
}

func attr(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}
	return ""
}

// walkPreorder visits every node under root in document order without
// recursion.
func walkPreorder(root *html.Node, visit func(*html.Node)) {
	walkSubtree(root, func(node *html.Node) bool {
		visit(node)
		return true
	})
}

// walkSubtree visits node and its descendants in document order without
// recursion. visit returns false to stop the walk.
func walkSubtree(root *html.Node, visit func(*html.Node) bool) {
	stack := []*html.Node{root}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !visit(node) {
			return
		}
		// Push children in reverse so the first child is visited first.
		var children []*html.Node
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			children = append(children, child)
		}
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, children[i])
		}
	}
}

// canonicalURI returns the browsable URI of a page. A provider link that is not
// a credential-free http(s) URI falls back to the REST URI of the page.
func canonicalURI(baseURL string, page Page) string {
	if uri := safeSourceURI(resolveWebUI(baseURL, page)); uri != "" {
		return uri
	}
	return restPageURI(baseURL, page.ID)
}

// resolveWebUI returns the browsable URL of a page from its `_links`.
//
// Atlassian documents Confluence Cloud's `_links.base` as the site URL with the
// /wiki context path and `_links.webui` as a path that starts with a slash, so
// the browsable URL is the two joined. Resolving the path against the base with
// RFC 3986 rules would replace the base path and drop /wiki. An absolute webui
// is used as is. A scheme-relative or other non-path webui yields "" so the
// caller falls back to the REST URI. The result is only a label on a fact: no
// request is made to it.
func resolveWebUI(baseURL string, page Page) string {
	if strings.HasPrefix(page.Links.WebUI, "http://") || strings.HasPrefix(page.Links.WebUI, "https://") {
		return page.Links.WebUI
	}
	base := firstNonEmpty(page.Links.Base, baseURL)
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return page.Links.WebUI
	}
	if strings.TrimSpace(page.Links.WebUI) == "" {
		return restPageURI(parsed.String(), page.ID)
	}
	relative, err := url.Parse(page.Links.WebUI)
	if err != nil {
		return ""
	}
	if relative.Scheme != "" || relative.Host != "" {
		return ""
	}
	rawPath := joinURLPath(parsed.EscapedPath(), relative.EscapedPath())
	path, err := url.PathUnescape(rawPath)
	if err != nil {
		return ""
	}
	joined := *parsed
	joined.Path, joined.RawPath = path, rawPath
	joined.RawQuery, joined.Fragment, joined.RawFragment = relative.RawQuery, relative.Fragment, relative.RawFragment
	return joined.String()
}

// joinURLPath appends the webui path to the base path with one slash between
// them. A webui path that already starts with the base path, as a whole
// segment, is not doubled.
func joinURLPath(basePath, webuiPath string) string {
	basePath = strings.TrimRight(basePath, "/")
	rooted := "/" + strings.TrimLeft(webuiPath, "/")
	if basePath != "" && (rooted == basePath || strings.HasPrefix(rooted, basePath+"/")) {
		return rooted
	}
	return basePath + rooted
}

func restPageURI(baseURL, pageID string) string {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return strings.TrimRight(baseURL, "/")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/v2/pages/" + pageID
	return parsed.String()
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func labelNames(labels []Label) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		if strings.TrimSpace(label.Name) != "" {
			out = append(out, label.Name)
		}
	}
	return out
}

func parentDocumentID(parentID string) string {
	if strings.TrimSpace(parentID) == "" {
		return ""
	}
	return "doc:confluence:" + parentID
}

func targetKind(href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return "external_uri"
	}
	return "confluence_reference"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
