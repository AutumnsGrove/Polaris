package llm

// LeakFilter exposes reasoningLeakSniffer to transports other than Client's
// own SSE reader (agent's Nova provider decorator), so a provider that leaks
// a reasoning control token into visible content gets the same re-filing
// treatment on either path instead of each keeping its own copy of the
// marker list and the arm/resolve state machine.
type LeakFilter struct {
	s *reasoningLeakSniffer
}

// NewLeakFilter builds a filter that sends confirmed-visible text to content
// and misrouted reasoning to reasoning. It starts armed, like Client's reader.
func NewLeakFilter(content, reasoning func(string)) *LeakFilter {
	f := &LeakFilter{s: &reasoningLeakSniffer{content: content, reasoning: reasoning}}
	f.s.arm()
	return f
}

// Content feeds one visible-content chunk.
func (f *LeakFilter) Content(chunk string) { f.s.onChunk(chunk) }

// ReasoningSeen re-arms the filter; call it whenever a real reasoning chunk
// arrives (see reasoningLeakSniffer.arm for why that's the only boundary).
func (f *LeakFilter) ReasoningSeen() { f.s.arm() }

// Flush resolves anything still buffered. Call it once the stream ends.
func (f *LeakFilter) Flush() { f.s.flush() }
