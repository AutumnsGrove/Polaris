package oraclebench

import "testing"

func FuzzParseJudgment(f *testing.F) {
	f.Add(`{"acceptable":true,"quality":0.9}`)
	f.Add("```json\n{\"acceptable\":false,\"quality\":0.9}\n```")
	f.Add(`{"quality":7}`)
	f.Add(`}{`)
	f.Fuzz(func(t *testing.T, content string) {
		j, err := parseJudgment(content)
		if err != nil {
			return
		}
		if j.Quality < 0 || j.Quality > 1 {
			t.Errorf("quality %v outside [0,1]", j.Quality)
		}
		if !j.Acceptable && j.Quality > 0.25 {
			t.Errorf("unacceptable judgment has quality %v", j.Quality)
		}
	})
}
