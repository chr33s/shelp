package embedding

import (
	"context"
	"reflect"
	"testing"
)

func TestLocalModelAndRoundTrip(t *testing.T) {
	texts := []string{"find files by size disk storage", "du disk storage usage files", "git commit history revision", "log revision history commit"}
	model, vectors, err := Train(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := Train(context.Background(), texts)
	if err != nil || !reflect.DeepEqual(model, again) {
		t.Fatal("model is not deterministic", err)
	}
	query := model.Embed("size")
	related, _ := Cosine(query, vectors[1])
	unrelated, _ := Cosine(query, vectors[2])
	if related <= unrelated || related < 0.3 {
		t.Fatalf("semantic ranking: %f <= %f", related, unrelated)
	}
	for _, v := range vectors {
		decoded, err := Decode(Encode(v))
		if err != nil {
			t.Fatal(err)
		}
		similarity, err := Cosine(v, decoded)
		if err != nil || similarity < 0.9999 {
			t.Fatalf("float32 round trip: %f %v", similarity, err)
		}
	}
	if len(model.Embed("unseenword")) != 0 {
		t.Fatal("unseen query should have no vector")
	}
	if _, err = Decode([]byte{1}); err == nil {
		t.Fatal("invalid blob accepted")
	}
}
func TestEmptyAndCancelledCorpus(t *testing.T) {
	for _, texts := range [][]string{nil, {""}, {"a"}, {"same same", "same same"}} {
		_, v, err := Train(context.Background(), texts)
		if err != nil || len(v) != len(texts) {
			t.Fatal(v, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Train(ctx, []string{"text"}); err == nil {
		t.Fatal("cancelled training succeeded")
	}
}

func TestStemMatchesPorter(t *testing.T) {
	// Reference pairs from Porter's paper and SQLite's FTS5 porter tokenizer.
	for word, want := range map[string]string{
		"caresses": "caress", "ponies": "poni", "cats": "cat", "feed": "feed", "agreed": "agre",
		"plastered": "plaster", "motoring": "motor", "hopping": "hop", "filing": "file", "happy": "happi",
		"relational": "relat", "conditional": "condit", "generalization": "gener", "electrical": "electr",
		"adjustment": "adjust", "files": "file", "searching": "search", "searcher": "searcher",
		"directories": "directori", "assembly": "assembl", "technology": "technolog", "go": "go", "naïve": "naïve",
	} {
		if got := Stem(word); got != want {
			t.Errorf("Stem(%q) = %q, want %q", word, got, want)
		}
	}
}

func TestTokensStemStopAndFold(t *testing.T) {
	tokens := Tokens("Usage: grep [options] searching Files")
	for _, gone := range []string{"option", "options"} {
		if _, ok := tokens[gone]; ok {
			t.Fatalf("help boilerplate kept: %v", tokens)
		}
	}
	if tokens["search"] != 1 || tokens["file"] != 1 {
		t.Fatalf("stems: %v", tokens)
	}
	counts := []map[string]float64{{"searcher": 1, "filter": 1}, {"search": 2}}
	fold(counts)
	if counts[0]["search"] != 1 || counts[0]["searcher"] != 0 || counts[0]["filter"] != 1 {
		t.Fatalf("fold: %v", counts)
	}
}
