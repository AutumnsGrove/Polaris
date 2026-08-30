// Package benchmark loads OpenAI's BrowseComp dataset and grades a
// harness's answers against it, mirroring openai/simple-evals'
// browsecomp_eval.py closely enough to produce comparable numbers: same
// decrypt algorithm, same query/grader prompt templates, same
// fail-closed "correct: no" default when the grader's response doesn't
// parse.
package benchmark

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
)

// Row is one decrypted BrowseComp question. Problem/Answer are already
// decrypted — see LoadDataset — never the raw ciphertext from the CSV.
type Row struct {
	Problem      string
	Answer       string
	ProblemTopic string
	Canary       string
}

// deriveKey is a direct port of browsecomp_eval.py's derive_key: a
// SHA-256 digest of the password, repeated and truncated to exactly
// length bytes so it can be XORed against a ciphertext of any size.
func deriveKey(password string, length int) []byte {
	sum := sha256.Sum256([]byte(password))
	digest := sum[:]
	key := make([]byte, length)
	for i := range key {
		key[i] = digest[i%len(digest)]
	}
	return key
}

// decrypt reverses BrowseComp's per-row XOR encryption. password is that
// row's own canary string — every row is encrypted with a different key,
// so a canary only ever decrypts its own row.
func decrypt(ciphertextB64, password string) (string, error) {
	encrypted, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", fmt.Errorf("decoding base64: %w", err)
	}
	key := deriveKey(password, len(encrypted))
	decrypted := make([]byte, len(encrypted))
	for i := range encrypted {
		decrypted[i] = encrypted[i] ^ key[i]
	}
	return string(decrypted), nil
}

// LoadDataset reads BrowseComp's CSV (problem, answer, problem_topic,
// canary columns — the shape of the Kaggle-published
// browse_comp_test_set.csv) and decrypts problem/answer per row using
// that row's own canary.
func LoadDataset(csvPath string) ([]Row, error) {
	f, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("opening dataset: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[name] = i
	}
	for _, required := range []string{"problem", "answer", "canary"} {
		if _, ok := col[required]; !ok {
			return nil, fmt.Errorf("dataset missing required column %q", required)
		}
	}

	var rows []Row
	for {
		record, err := r.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("reading row %d: %w", len(rows)+1, err)
		}
		canary := record[col["canary"]]
		problem, err := decrypt(record[col["problem"]], canary)
		if err != nil {
			return nil, fmt.Errorf("decrypting problem for row %d: %w", len(rows)+1, err)
		}
		answer, err := decrypt(record[col["answer"]], canary)
		if err != nil {
			return nil, fmt.Errorf("decrypting answer for row %d: %w", len(rows)+1, err)
		}
		row := Row{Problem: problem, Answer: answer, Canary: canary}
		if i, ok := col["problem_topic"]; ok && i < len(record) {
			row.ProblemTopic = record[i]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Sample picks n rows deterministically for a given seed — the same
// (rows, n, seed) always yields the same subset, so repeated runs of a
// benchmark pilot are testing the identical question set.
func Sample(rows []Row, n int, seed int64) []Row {
	if n <= 0 || n >= len(rows) {
		return rows
	}
	rng := rand.New(rand.NewSource(seed))
	shuffled := make([]Row, len(rows))
	copy(shuffled, rows)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	return shuffled[:n]
}
