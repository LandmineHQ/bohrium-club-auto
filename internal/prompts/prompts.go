package prompts

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
)

//go:embed data/science_space_prompts.json data/academic_writing_prompts.json data/literature_reading_prompts.json data/playground_prompts.json data/papers_catalog.json data/scimaster_system_prompt.txt data/desktop_tools.json
var embeddedFS embed.FS

// Paper represents one real open-access scientific paper PDF in the pre-collected 100-paper catalog.
type Paper struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Authors         []string `json:"authors"`
	Category        string   `json:"category"`
	Published       string   `json:"published"`
	PDFURL          string   `json:"pdfUrl"`
	MirrorPDFURL    string   `json:"mirrorPdfUrl"`
	Filename        string   `json:"filename"`
	Pages           int      `json:"pages"`
	AbstractSnippet string   `json:"abstractSnippet"`
}

// Stats summarizes embedded prompt and paper pool counts.
type Stats struct {
	ScienceSpacePrompts      int `json:"scienceSpacePrompts"`
	AcademicWritingPrompts   int `json:"academicWritingPrompts"`
	LiteratureReadingPrompts int `json:"literatureReadingPrompts"`
	PlaygroundPrompts        int `json:"playgroundPrompts"`
	PaperCatalogCount        int `json:"paperCatalogCount"`
}

var (
	loadOnce sync.Once
	loadErr  error

	scienceSpacePrompts      []string
	academicWritingPrompts   []string
	literatureReadingPrompts []string
	playgroundPrompts        []string
	papersCatalog            []Paper
	sciMasterSystemPrompt    string
	desktopTools             []map[string]any
)

func ensureLoaded() error {
	loadOnce.Do(func() {
		if err := loadJSON("data/science_space_prompts.json", &scienceSpacePrompts); err != nil {
			loadErr = err
			return
		}
		if err := loadJSON("data/academic_writing_prompts.json", &academicWritingPrompts); err != nil {
			loadErr = err
			return
		}
		if err := loadJSON("data/literature_reading_prompts.json", &literatureReadingPrompts); err != nil {
			loadErr = err
			return
		}
		if err := loadJSON("data/playground_prompts.json", &playgroundPrompts); err != nil {
			loadErr = err
			return
		}
		if err := loadJSON("data/papers_catalog.json", &papersCatalog); err != nil {
			loadErr = err
			return
		}
		rawPrompt, err := embeddedFS.ReadFile("data/scimaster_system_prompt.txt")
		if err != nil {
			loadErr = fmt.Errorf("read embedded scimaster_system_prompt.txt: %w", err)
			return
		}
		sciMasterSystemPrompt = strings.TrimSpace(string(rawPrompt))
		if err := loadJSON("data/desktop_tools.json", &desktopTools); err != nil {
			loadErr = err
			return
		}
	})
	return loadErr
}

func loadJSON(name string, dst any) error {
	raw, err := embeddedFS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", name, err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("parse embedded %s: %w", name, err)
	}
	return nil
}

// GetStats returns the sizes of the embedded prompt corpora and paper catalog.
func GetStats() Stats {
	_ = ensureLoaded()
	return Stats{
		ScienceSpacePrompts:      len(scienceSpacePrompts),
		AcademicWritingPrompts:   len(academicWritingPrompts),
		LiteratureReadingPrompts: len(literatureReadingPrompts),
		PlaygroundPrompts:        len(playgroundPrompts),
		PaperCatalogCount:        len(papersCatalog),
	}
}

// PlaygroundPrompts returns a copy of all 100 embedded OpenAI gateway playground test prompts.
func PlaygroundPrompts() ([]string, error) {
	if err := ensureLoaded(); err != nil {
		return nil, err
	}
	out := make([]string, len(playgroundPrompts))
	copy(out, playgroundPrompts)
	return out, nil
}

// SciMasterSystemPrompt returns Bohrium desktop client's native SciMaster persona system prompt.
func SciMasterSystemPrompt() string {
	_ = ensureLoaded()
	return sciMasterSystemPrompt
}

// DesktopClientTools returns a deep-cloned slice of all 16 Bohrium desktop client built-in OpenAI function tools
// matching scix v0.10.25 (cordis.yml / brain.mjs).
func DesktopClientTools() ([]map[string]any, error) {
	if err := ensureLoaded(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(desktopTools)
	if err != nil {
		return nil, err
	}
	var cloned []map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

// RandomPlaygroundPrompt picks a random prompt from the 100-item playground pool.
func RandomPlaygroundPrompt() (string, error) {
	if err := ensureLoaded(); err != nil {
		return "", err
	}
	return pickRandom(playgroundPrompts)
}

// RandomScienceSpacePrompt picks a random scientific question for Task 1.
func RandomScienceSpacePrompt() (string, error) {
	if err := ensureLoaded(); err != nil {
		return "", err
	}
	return pickRandom(scienceSpacePrompts)
}

// RandomAcademicWritingPrompt picks a random academic writing prompt for Task 4.
func RandomAcademicWritingPrompt() (string, error) {
	if err := ensureLoaded(); err != nil {
		return "", err
	}
	return pickRandom(academicWritingPrompts)
}

// RandomLiteratureReadingPrompt picks a random PDF reading question for Task 3.
func RandomLiteratureReadingPrompt() (string, error) {
	if err := ensureLoaded(); err != nil {
		return "", err
	}
	return pickRandom(literatureReadingPrompts)
}

// Papers returns a copy of the 100-paper catalog.
func Papers() ([]Paper, error) {
	if err := ensureLoaded(); err != nil {
		return nil, err
	}
	out := make([]Paper, len(papersCatalog))
	copy(out, papersCatalog)
	return out, nil
}

// RandomPaper picks a random paper from the 100-paper catalog.
func RandomPaper() (Paper, error) {
	if err := ensureLoaded(); err != nil {
		return Paper{}, err
	}
	return pickRandom(papersCatalog)
}

// ShuffledPapers returns a cryptographically shuffled copy of the 100-paper catalog
// so callers can fall back seamlessly if a single upstream PDF download fails.
func ShuffledPapers() ([]Paper, error) {
	papers, err := Papers()
	if err != nil {
		return nil, err
	}
	for i := len(papers) - 1; i > 0; i-- {
		j, err := cryptoRandInt(i + 1)
		if err != nil {
			return papers, nil
		}
		papers[i], papers[j] = papers[j], papers[i]
	}
	return papers, nil
}

func pickRandom[T any](items []T) (T, error) {
	var zero T
	if len(items) == 0 {
		return zero, fmt.Errorf("prompt/paper corpus is empty")
	}
	idx, err := cryptoRandInt(len(items))
	if err != nil {
		return items[0], nil
	}
	return items[idx], nil
}

func cryptoRandInt(n int) (int, error) {
	if n <= 1 {
		return 0, nil
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}
