package reddit

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// thing/listing mirror Reddit's generic {"kind": ..., "data": ...} envelope.
// A thread response is a two-element JSON array (post listing, comment
// listing); a subreddit/search response is a single listing — formatAPI
// tells them apart by the first byte.
type thing struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type listing struct {
	Data struct {
		Children []thing `json:"children"`
	} `json:"data"`
}

type post struct {
	Title       string  `json:"title"`
	Subreddit   string  `json:"subreddit_name_prefixed"`
	Author      string  `json:"author"`
	Score       int     `json:"score"`
	NumComments int     `json:"num_comments"`
	CreatedUTC  float64 `json:"created_utc"`
	Selftext    string  `json:"selftext"`
	URL         string  `json:"url"`
	IsSelf      bool    `json:"is_self"`
	Permalink   string  `json:"permalink"`
}

type comment struct {
	Author  string          `json:"author"`
	Score   int             `json:"score"`
	Body    string          `json:"body"`
	Replies json.RawMessage `json:"replies"` // "" when there are none, a listing otherwise
}

func formatAPI(body []byte) (title, text string, err error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", "", fmt.Errorf("empty api response")
	}

	var b strings.Builder
	if trimmed[0] == '[' {
		var listings []listing
		if err := json.Unmarshal(trimmed, &listings); err != nil {
			return "", "", fmt.Errorf("parsing thread: %w", err)
		}
		if len(listings) < 2 || len(listings[0].Data.Children) == 0 {
			return "", "", fmt.Errorf("thread response had no post")
		}
		var p post
		if err := json.Unmarshal(listings[0].Data.Children[0].Data, &p); err != nil {
			return "", "", fmt.Errorf("parsing post: %w", err)
		}
		writePostHeader(&b, p)
		b.WriteString("\n\nComments (best first):\n")
		n := writeComments(&b, listings[1].Data.Children, 0)
		if n == 0 {
			b.WriteString("(no comments)\n")
		}
		return p.Title, strings.TrimSpace(b.String()), nil
	}

	var l listing
	if err := json.Unmarshal(trimmed, &l); err != nil {
		return "", "", fmt.Errorf("parsing listing: %w", err)
	}
	i := 0
	for _, c := range l.Data.Children {
		if c.Kind != "t3" {
			continue
		}
		var p post
		if json.Unmarshal(c.Data, &p) != nil {
			continue
		}
		i++
		fmt.Fprintf(&b, "%d. %s\n   %s · u/%s · %d points · %d comments · %s\n   https://www.reddit.com%s\n",
			i, p.Title, p.Subreddit, p.Author, p.Score, p.NumComments, ago(p.CreatedUTC), p.Permalink)
		if s := strings.TrimSpace(p.Selftext); s != "" {
			fmt.Fprintf(&b, "   %s\n", snippet(s, 300))
		}
		b.WriteString("\n")
	}
	if i == 0 {
		return "", "", fmt.Errorf("listing had no posts")
	}
	return "Reddit listing", strings.TrimSpace(b.String()), nil
}

func writePostHeader(b *strings.Builder, p post) {
	fmt.Fprintf(b, "%s\n%s · u/%s · %d points · %d comments · %s", p.Title, p.Subreddit, p.Author, p.Score, p.NumComments, ago(p.CreatedUTC))
	if !p.IsSelf && p.URL != "" {
		fmt.Fprintf(b, "\nLinks to: %s", p.URL)
	}
	if s := strings.TrimSpace(p.Selftext); s != "" {
		b.WriteString("\n\n" + s)
	}
}

// writeComments renders a comment tree depth-first, indenting two spaces per
// level, and returns how many comments it wrote. "more" stubs (collapsed
// replies Reddit would need a second request to expand) are skipped.
func writeComments(b *strings.Builder, children []thing, depth int) int {
	n := 0
	for _, c := range children {
		if c.Kind != "t1" {
			continue
		}
		var cm comment
		if json.Unmarshal(c.Data, &cm) != nil {
			continue
		}
		indent := strings.Repeat("  ", depth)
		body := strings.ReplaceAll(strings.TrimSpace(cm.Body), "\n", "\n"+indent+"  ")
		fmt.Fprintf(b, "%s[u/%s · %d] %s\n", indent, cm.Author, cm.Score, body)
		n++
		// Replies is "" (a JSON string) for a leaf comment, a listing otherwise.
		var rl listing
		if len(cm.Replies) > 0 && cm.Replies[0] == '{' && json.Unmarshal(cm.Replies, &rl) == nil {
			n += writeComments(b, rl.Data.Children, depth+1)
		}
	}
	return n
}

func ago(created float64) string {
	if created <= 0 {
		return "unknown date"
	}
	return time.Unix(int64(created), 0).UTC().Format("2006-01-02")
}

func snippet(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// --- RSS tier ---

type atomFeed struct {
	Title   string `xml:"title"`
	Entries []struct {
		Title  string `xml:"title"`
		Author struct {
			Name string `xml:"name"`
		} `xml:"author"`
		Updated string `xml:"updated"`
		Content string `xml:"content"`
	} `xml:"entry"`
}

func (c *Client) fetchRSS(ctx context.Context, p redditPath) (*Result, error) {
	endpoint := c.rssBase + p.path + "/.rss"
	if len(p.query) > 0 {
		endpoint += "?" + p.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)

	body, status, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests {
		return nil, fmt.Errorf("feed rate-limited by Reddit (status 429)")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("feed returned status %d", status)
	}

	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parsing feed: %w", err)
	}
	if len(feed.Entries) == 0 {
		return nil, fmt.Errorf("feed had no entries")
	}

	var b strings.Builder
	for i, e := range feed.Entries {
		author := strings.TrimPrefix(e.Author.Name, "/u/")
		date := e.Updated
		if len(date) >= 10 {
			date = date[:10]
		}
		fmt.Fprintf(&b, "[%d] %s — u/%s · %s\n", i+1, e.Title, author, date)
		if t := htmlToText(e.Content); t != "" {
			b.WriteString(t + "\n")
		}
		b.WriteString("\n")
	}
	title := strings.TrimSpace(feed.Title)
	if title == "" {
		title = p.path
	}
	return &Result{Title: title, Text: strings.TrimSpace(b.String()), Source: "reddit rss"}, nil
}

// htmlToText flattens a feed entry's HTML body. Reddit wraps the actual
// post/comment text in <div class="md"> and surrounds it with table markup
// plus "submitted by … [link] [comments]" boilerplate, so take just the md
// block when it exists (link-only posts have none, hence the whole-document
// fallback with the boilerplate lines filtered out).
func htmlToText(h string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(h))
	if err != nil {
		return strings.TrimSpace(h)
	}
	doc.Find("script, style").Remove()
	root := doc.Find("div.md")
	if root.Length() == 0 {
		root = doc.Selection
	}
	var lines []string
	for _, ln := range strings.Split(root.Text(), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || ln == "[link]" || ln == "[comments]" || strings.HasPrefix(ln, "submitted by") {
			continue
		}
		lines = append(lines, ln)
	}
	return strings.Join(lines, "\n")
}
