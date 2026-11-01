package main

import (
	"fmt"
	"io"
	"time"

	"github.com/chr33s/shelp/internal/index"
)

// Progress is line-oriented on stderr so stdout remains suitable for scripts.
func indexProgress(w io.Writer) func(index.Progress) {
	var last time.Time
	return func(p index.Progress) {
		switch p.Stage {
		case "scan":
			fmt.Fprintln(w, "Scanning PATH...")
		case "capture":
			if p.Done != 0 && p.Done != p.Total && time.Since(last) < time.Second {
				return
			}
			fmt.Fprintf(w, "Capturing help: %d/%d\n", p.Done, p.Total)
			last = time.Now()
		case "train":
			fmt.Fprintln(w, "Training embeddings...")
		case "save":
			fmt.Fprintln(w, "Saving index...")
		}
	}
}
