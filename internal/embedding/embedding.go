// Package embedding trains a local latent semantic analysis (LSA) model.
// It learns term relationships from the indexed help corpus, without a server,
// pretrained weights, or network access. Queries use the same TF-IDF projection.
package embedding

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"unicode"

	"gonum.org/v1/gonum/mat"
)

const Dimensions = 64
const maxTerms = 4096

type Term struct {
	IDF    float64
	Vector []float64
}
type Model map[string]Term

var stopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range strings.Fields("a an and are as at be been but by can do for from had has have if in into is it its may not of on or our out per so than that the their then there these they this those to use used using was we were when where which who will with you your") {
		m[s] = true
	}
	return m
}()

func Tokens(text string) map[string]float64 {
	counts := map[string]float64{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len(word) < 2 || len(word) > 64 || stopWords[word] {
			continue
		}
		if len(word) > 4 && strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") {
			word = strings.TrimSuffix(word, "s")
		}
		counts[word]++
	}
	return counts
}

// Train uses randomized truncated SVD, with a fixed seed for repeatable output.
// The sparse document matrix and rank-limited projection bound memory use.
func Train(ctx context.Context, texts []string) (Model, [][]float64, error) {
	counts := make([]map[string]float64, len(texts))
	df := map[string]int{}
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		counts[i] = Tokens(text)
		for word := range counts[i] {
			df[word]++
		}
	}
	words := make([]string, 0, len(df))
	for word := range df {
		words = append(words, word)
	}
	sort.Slice(words, func(i, j int) bool {
		if df[words[i]] == df[words[j]] {
			return words[i] < words[j]
		}
		return df[words[i]] > df[words[j]]
	})
	if len(words) > maxTerms {
		words = words[:maxTerms]
	}
	sort.Strings(words)
	model := Model{}
	if len(words) == 0 {
		return model, make([][]float64, len(texts)), nil
	}
	idf := make([]float64, len(words))
	for j, word := range words {
		idf[j] = math.Log(float64(len(texts)+1)/float64(df[word]+1)) + 1
	}
	type entry struct {
		col    int
		weight float64
	}
	rows := make([][]entry, len(texts))
	for i, c := range counts {
		norm := 0.0
		// Stable order avoids map iteration changing floating-point sums.
		for j, word := range words {
			if n := c[word]; n > 0 {
				w := (1 + math.Log(n)) * idf[j]
				rows[i] = append(rows[i], entry{j, w})
				norm += w * w
			}
		}
		norm = math.Sqrt(norm)
		for k := range rows[i] {
			rows[i][k].weight /= norm
		}
	}
	rank := min(Dimensions+8, len(texts), len(words))
	random := rand.New(rand.NewSource(1))
	omega := make([][]float64, len(words))
	for j := range omega {
		omega[j] = make([]float64, rank)
		for k := range omega[j] {
			omega[j][k] = random.NormFloat64()
		}
	}
	y := make([][]float64, len(texts))
	for i, row := range rows {
		y[i] = make([]float64, rank)
		for _, e := range row {
			for k, v := range omega[e.col] {
				y[i][k] += e.weight * v
			}
		}
	}
	q := orthogonal(y, rank)
	// One power iteration improves approximation of the leading semantic axes.
	for iteration := 0; iteration < 1 && len(q) > 0; iteration++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		z := make([][]float64, len(words))
		for j := range z {
			z[j] = make([]float64, len(q))
		}
		for i, row := range rows {
			for _, e := range row {
				for k := range q {
					z[e.col][k] += e.weight * q[k][i]
				}
			}
		}
		for i, row := range rows {
			y[i] = make([]float64, len(q))
			for _, e := range row {
				for k, v := range z[e.col] {
					y[i][k] += e.weight * v
				}
			}
		}
		q = orthogonal(y, len(q))
	}
	if len(q) == 0 {
		return model, make([][]float64, len(texts)), nil
	}
	b := mat.NewDense(len(q), len(words), nil)
	for i, row := range rows {
		for _, e := range row {
			for k := range q {
				b.Set(k, e.col, b.At(k, e.col)+q[k][i]*e.weight)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var svd mat.SVD
	if !svd.Factorize(b, mat.SVDThin) {
		return nil, nil, fmt.Errorf("local embedding model SVD failed")
	}
	values := svd.Values(nil)
	dim := min(Dimensions, len(values), max(1, len(texts)/2))
	for dim > 0 && values[dim-1] < 1e-8 {
		dim--
	}
	var v mat.Dense
	svd.VTo(&v)
	for j, word := range words {
		vector := make([]float64, dim)
		for k := range vector {
			vector[k] = v.At(j, k)
		}
		model[word] = Term{IDF: idf[j], Vector: vector}
	}
	vectors := make([][]float64, len(texts))
	for i, text := range texts {
		vectors[i] = model.Embed(text)
	}
	return model, vectors, ctx.Err()
}

// orthogonal returns a rank-revealing, reorthogonalized basis as columns.
func orthogonal(y [][]float64, rank int) [][]float64 {
	var q [][]float64
	for k := 0; k < rank; k++ {
		column := make([]float64, len(y))
		for i := range y {
			column[i] = y[i][k]
		}
		for pass := 0; pass < 2; pass++ {
			for _, basis := range q {
				dot := 0.0
				for i, x := range column {
					dot += x * basis[i]
				}
				for i := range column {
					column[i] -= dot * basis[i]
				}
			}
		}
		norm := 0.0
		for _, x := range column {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		if norm < 1e-10 {
			continue
		}
		for i := range column {
			column[i] /= norm
		}
		q = append(q, column)
	}
	return q
}

func (m Model) Embed(text string) []float64 {
	counts := Tokens(text)
	words := make([]string, 0, len(counts))
	for word := range counts {
		words = append(words, word)
	}
	sort.Strings(words)
	var result []float64
	for _, word := range words {
		term, ok := m[word]
		if !ok {
			continue
		}
		if result == nil {
			result = make([]float64, len(term.Vector))
		}
		w := (1 + math.Log(counts[word])) * term.IDF
		for k, x := range term.Vector {
			result[k] += w * x
		}
	}
	return result
}

func Encode(v []float64) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(float32(x)))
	}
	return b
}
func Decode(b []byte) ([]float64, error) {
	if len(b)%4 != 0 || len(b) > Dimensions*4 {
		return nil, fmt.Errorf("invalid embedding blob length %d", len(b))
	}
	v := make([]float64, len(b)/4)
	for i := range v {
		v[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:])))
		if math.IsNaN(v[i]) || math.IsInf(v[i], 0) {
			return nil, fmt.Errorf("invalid embedding value")
		}
	}
	return v, nil
}
func Cosine(a, b []float64) (float64, error) {
	if len(a) == 0 || len(b) == 0 {
		return 0, nil
	}
	if len(a) != len(b) {
		return 0, fmt.Errorf("embedding dimensions differ; run shelp index --full")
	}
	var dot, aa, bb float64
	for i, x := range a {
		dot += x * b[i]
		aa += x * x
		bb += b[i] * b[i]
	}
	if aa == 0 || bb == 0 {
		return 0, nil
	}
	return dot / math.Sqrt(aa) / math.Sqrt(bb), nil
}
