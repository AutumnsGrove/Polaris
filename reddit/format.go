package reddit

// Rendering. Everything here works on Reddit's own object shapes (t3 posts, t1
// comments with Listing-wrapped replies), which is what makes the archive tier
// cheap: Arctic Shift hands back the same shapes the Data API did, so this code
// survived Reddit shutting that API's doors and will survive the archive being
// replaced by the next source.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// thing/listing mirror Reddit's generic {"kind": ..., "data": ...} envelope.
// A thread response is a two-element JSON array (post listing, comment
// listing); a subreddit/search response is a single listing — formatAPI tells
// them apart by the first byte.
type thing struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// listing is only here to unwrap a comment's replies.
type listing struct {
	Data struct {
		Children []thing `json:"children"`
	} `json:"data"`
}

type post struct {
	Title       string  `json:"title"`
	Subreddit   string  `json:"subreddit_name_prefixed"`
	SubredditID string  `json:"subreddit"` // bare name; the archive always has this, the prefixed field is the prettier one
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
	Replies json.RawMessage `json:"replies"` // "" when there are none, a Listing otherwise
}

// renderThread writes a post header, any caller-supplied caveat, and the
// comment tree.
func renderThread(p post, comments []thing, note string) (title, text string) {
	var b strings.Builder
	writePostHeader(&b, p)
	if note != "" {
		b.WriteString("\n\nNote: " + note)
	}
	b.WriteString("\n\nComments:\n")
	n := writeComments(&b, comments, 0)
	switch {
	case n == 0 && p.NumComments > 0:
		// Reddit counted replies the archive hasn't handed back yet. Saying
		// "no comments" here would be a flat lie, and the model would answer
		// as if nobody had replied.
		fmt.Fprintf(&b, "(no comments in this archive copy, though Reddit counts %d — recent replies may not be indexed yet)\n", p.NumComments)
	case n == 0:
		b.WriteString("(no comments)\n")
	case p.NumComments > n:
		// Worth separating from "nobody replied": the thread has more replies
		// than we rendered, whether because of the fetch cap or because the
		// archive hasn't indexed them yet.
		fmt.Fprintf(&b, "\n(%d more comments not shown)\n", p.NumComments-n)
	}
	return p.Title, strings.TrimSpace(b.String())
}

// renderListing writes numbered posts with their thread links. ok is false when
// nothing could be rendered, which callers turn into ErrNotArchived.
func renderListing(posts []post, note string) (text string, ok bool) {
	var b strings.Builder
	if note != "" {
		b.WriteString(note + "\n\n")
	}
	for i, p := range posts {
		fmt.Fprintf(&b, "%d. %s\n   %s · u/%s · %d points · %d comments · %s\n   https://www.reddit.com%s\n",
			i+1, p.Title, subredditLabel(p), p.Author, p.Score, p.NumComments, ago(p.CreatedUTC), p.Permalink)
		if s := strings.TrimSpace(p.Selftext); s != "" {
			fmt.Fprintf(&b, "   %s\n", snippet(s, 300))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), len(posts) > 0
}

func writePostHeader(b *strings.Builder, p post) {
	fmt.Fprintf(b, "%s\n%s · u/%s · %d points · %d comments · %s", p.Title, subredditLabel(p), p.Author, p.Score, p.NumComments, ago(p.CreatedUTC))
	if !p.IsSelf && p.URL != "" {
		fmt.Fprintf(b, "\nLinks to: %s", p.URL)
	}
	if s := strings.TrimSpace(p.Selftext); s != "" {
		b.WriteString("\n\n" + s)
	}
}

func subredditLabel(p post) string {
	if p.Subreddit != "" {
		return p.Subreddit
	}
	if p.SubredditID != "" {
		return "r/" + p.SubredditID
	}
	return "r/?"
}

// writeComments renders a comment tree depth-first, indenting two spaces per
// level, and returns how many comments it wrote. "more" stubs (collapsed
// replies that would need another request to expand) are skipped.
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
		n += writeComments(b, replyChildren(cm.Replies), depth+1)
	}
	return n
}

// replyChildren unwraps a comment's replies. A leaf is "" or "{}" depending on
// the tier, otherwise it's a Listing — handle every shape rather than branching
// per source, since the archive mimics the live API here.
func replyChildren(raw json.RawMessage) []thing {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "{}" {
		return nil
	}
	switch s[0] {
	case '[':
		var kids []thing
		if json.Unmarshal(raw, &kids) == nil {
			return kids
		}
	case '{':
		var l listing
		if json.Unmarshal(raw, &l) == nil {
			return l.Data.Children
		}
	}
	return nil
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
