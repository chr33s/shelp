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
