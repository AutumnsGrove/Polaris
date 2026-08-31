package benchmark

import "fmt"

// Suite is the seam between one benchmark's dataset/grading vocabulary
// and everything that doesn't need to know which suite is running:
// cmd/benchmark.go's sampling, progress printing, and tracking-DB writes
// operate on Row and a Suite uniformly, regardless of whether loading a
// row meant decrypting BrowseComp's XOR-ciphertext CSV or just reading a
// plain SimpleQA CSV column, and regardless of whether grading produces
// BrowseComp's binary yes/no or SimpleQA's three-way
// CORRECT/INCORRECT/NOT_ATTEMPTED.
type Suite interface {
	// Name is persisted verbatim into RunRecord.Benchmark.
	Name() string
	LoadDataset(path string) ([]Row, error)
	BuildQuery(question string) string
	BuildGraderPrompt(question, response, correctAnswer string) string
	Grade(graderResponse string) (correct bool, verdict string)
}

type browseCompSuite struct{}

func (browseCompSuite) Name() string                           { return "browsecomp" }
func (browseCompSuite) LoadDataset(path string) ([]Row, error) { return LoadDataset(path) }
func (browseCompSuite) BuildQuery(question string) string      { return BuildQuery(question) }
func (browseCompSuite) BuildGraderPrompt(question, response, correctAnswer string) string {
	return BuildGraderPrompt(question, response, correctAnswer)
}
func (browseCompSuite) Grade(graderResponse string) (bool, string) { return Grade(graderResponse) }

type simpleQASuite struct{}

func (simpleQASuite) Name() string                           { return "simpleqa" }
func (simpleQASuite) LoadDataset(path string) ([]Row, error) { return LoadSimpleQADataset(path) }
func (simpleQASuite) BuildQuery(question string) string      { return BuildSimpleQAQuery(question) }
func (simpleQASuite) BuildGraderPrompt(question, response, correctAnswer string) string {
	return BuildSimpleQAGraderPrompt(question, response, correctAnswer)
}
func (simpleQASuite) Grade(graderResponse string) (bool, string) {
	return GradeSimpleQA(graderResponse)
}

type liveNewsBenchSuite struct{}

func (liveNewsBenchSuite) Name() string { return "livenewsbench" }
func (liveNewsBenchSuite) LoadDataset(path string) ([]Row, error) {
	return LoadLiveNewsBenchDataset(path)
}

// BuildQuery/BuildGraderPrompt/Grade are SimpleQA's, unmodified —
// LiveNewsBench's real dataset (see livenewsbench.go) carries no source
// URL to exclude, so there's no wrapping BuildQuery needs to add, and
// its README confirms the grader is a byte-for-byte reuse of SimpleQA's.
func (liveNewsBenchSuite) BuildQuery(question string) string { return BuildSimpleQAQuery(question) }
func (liveNewsBenchSuite) BuildGraderPrompt(question, response, correctAnswer string) string {
	return BuildSimpleQAGraderPrompt(question, response, correctAnswer)
}
func (liveNewsBenchSuite) Grade(graderResponse string) (bool, string) {
	return GradeSimpleQA(graderResponse)
}

// Suites maps --suite flag values to their implementation.
var Suites = map[string]Suite{
	"browsecomp":    browseCompSuite{},
	"simpleqa":      simpleQASuite{},
	"livenewsbench": liveNewsBenchSuite{},
}

// SuiteNames lists valid --suite values for flag help text and error
// messages, in a stable order.
func SuiteNames() []string {
	return []string{"browsecomp", "simpleqa", "livenewsbench"}
}

func LookupSuite(name string) (Suite, error) {
	s, ok := Suites[name]
	if !ok {
		return nil, fmt.Errorf("unknown benchmark suite %q (valid: %v)", name, SuiteNames())
	}
	return s, nil
}
