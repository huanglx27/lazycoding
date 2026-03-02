package feishu

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxCardTextLen is the safe maximum rune count for a single card markdown field.
const MaxCardTextLen = 3000

var (
	rePreCode    = regexp.MustCompile(`(?s)<pre><code(?:[^>]*)>(.*?)</code></pre>`)
	reBold       = regexp.MustCompile(`(?s)<b>(.*?)</b>`)
	reItalic     = regexp.MustCompile(`(?s)<i>(.*?)</i>`)
	reStrike     = regexp.MustCompile(`(?s)<s>(.*?)</s>`)
	reBlockquote = regexp.MustCompile(`(?s)<blockquote>(.*?)</blockquote>`)
	reLink       = regexp.MustCompile(`<a href="([^"]*)">(.*?)</a>`)
	reCode       = regexp.MustCompile(`<code>(.*?)</code>`)
	reTag        = regexp.MustCompile(`<[^>]+>`)
)

// htmlUnescape decodes the four HTML entities Telegram produces.
func htmlUnescape(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	return s
}

// TelegramHTMLToLarkMarkdown converts the Telegram-style HTML produced by
// lazycoding's renderer into Lark Markdown suitable for a lark_md card element.
func TelegramHTMLToLarkMarkdown(html string) string {
	if html == "" {
		return ""
	}

	// Extract <pre><code> blocks first to protect their content from later
	// inline substitutions. Use sentinel placeholders that cannot appear in HTML.
	type block struct{ ph, md string }
	var blocks []block

	result := rePreCode.ReplaceAllStringFunc(html, func(m string) string {
		inner := htmlUnescape(rePreCode.FindStringSubmatch(m)[1])
		ph := "\x00BLOCK" + string(rune(0xE000+len(blocks))) + "\x00"
		blocks = append(blocks, block{ph, "```\n" + inner + "\n```"})
		return ph
	})

	// Inline substitutions — order matters (links before plain text).
	result = reLink.ReplaceAllStringFunc(result, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		return "[" + htmlUnescape(sub[2]) + "](" + htmlUnescape(sub[1]) + ")"
	})
	result = reBold.ReplaceAllStringFunc(result, func(m string) string {
		return "**" + htmlUnescape(reBold.FindStringSubmatch(m)[1]) + "**"
	})
	result = reItalic.ReplaceAllStringFunc(result, func(m string) string {
		return "*" + htmlUnescape(reItalic.FindStringSubmatch(m)[1]) + "*"
	})
	result = reStrike.ReplaceAllStringFunc(result, func(m string) string {
		return "~~" + htmlUnescape(reStrike.FindStringSubmatch(m)[1]) + "~~"
	})
	result = reBlockquote.ReplaceAllStringFunc(result, func(m string) string {
		return "> " + htmlUnescape(reBlockquote.FindStringSubmatch(m)[1])
	})
	result = reCode.ReplaceAllStringFunc(result, func(m string) string {
		return "`" + htmlUnescape(reCode.FindStringSubmatch(m)[1]) + "`"
	})

	// Strip any remaining HTML tags, then unescape entities in plain text.
	result = reTag.ReplaceAllString(result, "")
	result = htmlUnescape(result)

	// Restore code block placeholders.
	for _, b := range blocks {
		result = strings.ReplaceAll(result, b.ph, b.md)
	}

	return result
}

// SplitText splits Lark Markdown text into chunks of at most MaxCardTextLen
// runes, preferring newline boundaries.
func SplitText(text string) []string {
	if utf8.RuneCountInString(text) == 0 {
		return []string{"*(empty)*"}
	}
	var chunks []string
	for utf8.RuneCountInString(text) > 0 {
		if utf8.RuneCountInString(text) <= MaxCardTextLen {
			chunks = append(chunks, text)
			break
		}
		runes := []rune(text)
		cut := MaxCardTextLen
		for i := cut - 1; i > MaxCardTextLen/2; i-- {
			if runes[i] == '\n' {
				cut = i
				break
			}
		}
		chunks = append(chunks, string(runes[:cut]))
		text = strings.TrimPrefix(string(runes[cut:]), "\n")
	}
	return chunks
}
