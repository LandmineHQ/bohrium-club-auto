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
	if stats.PlaygroundPrompts != 100 {
		t.Fatalf("expected 100 playground prompts, got %d", stats.PlaygroundPrompts)
	}
	pgList, err := PlaygroundPrompts()
	if err != nil || len(pgList) != 100 {
		t.Fatalf("PlaygroundPrompts(): err=%v len=%d", err, len(pgList))
	}
	seenPG := make(map[string]bool, len(pgList))
	for i, q := range pgList {
		if strings.TrimSpace(q) == "" || seenPG[q] {
			t.Fatalf("invalid or duplicate playground prompt[%d]: %q", i, q)
		}
		seenPG[q] = true
	}
	if q, err := RandomPlaygroundPrompt(); err != nil || len(q) < 6 {
		t.Fatalf("RandomPlaygroundPrompt failed: %v %q", err, q)
	}
	sciPrompt := SciMasterSystemPrompt()
	if !strings.HasPrefix(sciPrompt, "You are SciMaster,") || len(sciPrompt) < 500 {
		t.Fatalf("expected authentic SciMaster system prompt, got len=%d prefix=%q", len(sciPrompt), sciPrompt[:min(40, len(sciPrompt))])
	}
	desktopTools, err := DesktopClientTools()
	if err != nil || len(desktopTools) != 16 {
		t.Fatalf("DesktopClientTools(): err=%v len=%d (expected 16)", err, len(desktopTools))
	}
	wantTools := map[string]bool{
		"pwsh": true, "read": true, "write": true, "edit": true,
		"read_image": true, "glob": true, "grep": true, "skill": true,
		"web_search": true, "web_fetch": true, "paper_search": true, "generate_image": true,
		"memory_write": true, "ask_user_question": true, "subagent": true, "browser": true,
	}
	for i, item := range desktopTools {
		if item["type"] != "function" {
			t.Fatalf("desktop tool[%d] type=%v, want function", i, item["type"])
		}
		fn, _ := item["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if !wantTools[name] {
			t.Fatalf("unexpected desktop tool[%d] name=%q", i, name)
		}
		delete(wantTools, name)
		if fn["strict"] != false {
			t.Fatalf("desktop tool %s expected strict=false, got %v", name, fn["strict"])
		}
		params, _ := fn["parameters"].(map[string]any)
		if params["type"] != "object" {
			t.Fatalf("desktop tool %s expected parameters.type=object, got %v", name, params["type"])
		}
	}
	if len(wantTools) != 0 {
		t.Fatalf("missing desktop tools: %+v", wantTools)
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
