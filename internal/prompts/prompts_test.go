package prompts

import (
	"strings"
	"testing"
)

func TestEmbeddedCorpusAndPapers(t *testing.T) {
	stats := GetStats()
	if stats.ScienceSpacePrompts < 300 {
		t.Fatalf("expected >=300 science space prompts, got %d", stats.ScienceSpacePrompts)
	}
	if stats.AcademicWritingPrompts < 300 {
		t.Fatalf("expected >=300 academic writing prompts, got %d", stats.AcademicWritingPrompts)
	}
	if stats.LiteratureReadingPrompts < 200 {
		t.Fatalf("expected >=200 literature reading prompts, got %d", stats.LiteratureReadingPrompts)
	}
	if stats.PaperCatalogCount != 100 {
		t.Fatalf("expected 100 papers in catalog, got %d", stats.PaperCatalogCount)
	}

	papers, err := Papers()
	if err != nil {
		t.Fatalf("Papers(): %v", err)
	}
	for i, p := range papers {
		if p.ID == "" || p.Title == "" || !strings.HasPrefix(p.PDFURL, "https://arxiv.org/pdf/") || !strings.HasSuffix(p.Filename, ".pdf") {
			t.Fatalf("invalid paper[%d]: %+v", i, p)
		}
	}

	if q, err := RandomScienceSpacePrompt(); err != nil || len(q) < 10 {
		t.Fatalf("RandomScienceSpacePrompt failed: %v %q", err, q)
	}
	if q, err := RandomAcademicWritingPrompt(); err != nil || len(q) < 10 {
		t.Fatalf("RandomAcademicWritingPrompt failed: %v %q", err, q)
	}
	if q, err := RandomLiteratureReadingPrompt(); err != nil || len(q) < 10 {
		t.Fatalf("RandomLiteratureReadingPrompt failed: %v %q", err, q)
	}
}
