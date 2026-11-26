package bohrium

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bohrium-club-auto/internal/prompts"
)

const (
	minCachedPDFPool      = 3
	cachedPDFReusePercent = 80
	arxivDownloadTimeout  = 35 * time.Second
)

// SelectedPDF holds the binary payload and metadata for Task 3 (Literature Reading).
type SelectedPDF struct {
	Filename string
	Title    string
	PaperID  string
	Source   string // "custom_local" | "cache" | "cache_fallback" | "arxiv_download" | "synthetic_fallback"
	Data     []byte
}

// AcquireRandomPaperPDF selects a random scientific PDF for Task 3:
// 1. If user placed custom .pdf files in data/custom_pdfs/, randomly includes them.
// 2. When >= minCachedPDFPool valid cached PDFs exist, reuses the local PDF cache ~80% of the time
//    to avoid redundant slow cross-border arXiv downloads during interactive/scheduled runs.
// 3. Otherwise picks randomly from the embedded 100-paper arXiv catalog, reading from data/pdfs/ cache
//    or downloading via Chrome HTTP client (with mirror fallback and up to 6 catalog retries).
// 4. Only if all external downloads and cache fallbacks fail (and ctx is still active), generates a minimal valid PDF.
func (c *Client) AcquireRandomPaperPDF(ctx context.Context) (*SelectedPDF, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cfg := c.cfg.Get()

	// Check optional user-supplied local PDFs first.
	if customPDFs := listLocalPDFs(cfg.LocalPDFDir, cfg.MaxPDFSizeBytes); len(customPDFs) > 0 {
		idx := secureRandInt(len(customPDFs))
		p := customPDFs[idx]
		data, err := os.ReadFile(p)
		if err == nil && isValidPDF(data) {
			return &SelectedPDF{
				Filename: filepath.Base(p),
				Title:    strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)),
				PaperID:  "custom:" + filepath.Base(p),
				Source:   "custom_local",
				Data:     data,
			}, nil
		}
	}

	shuffled, err := prompts.ShuffledPapers()
	if err != nil {
		return nil, err
	}

	paperByFilename := make(map[string]prompts.Paper, len(shuffled))
	for _, p := range shuffled {
		paperByFilename[p.Filename] = p
	}

	cachedFiles := listLocalPDFs(cfg.PDFCacheDir, cfg.MaxPDFSizeBytes)
	if len(cachedFiles) >= minCachedPDFPool && secureRandInt(100) < cachedPDFReusePercent {
		if picked := loadCachedPaperPDF(cachedFiles, paperByFilename, "cache"); picked != nil {
			return picked, nil
		}
	}

	// Try up to 6 distinct random papers from our 100-paper catalog.
	maxAttempts := 6
	if len(shuffled) < maxAttempts {
		maxAttempts = len(shuffled)
	}
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		paper := shuffled[i]
		cachePath := filepath.Join(cfg.PDFCacheDir, paper.Filename)

		// Fast path: cached on disk already.
		if data, err := os.ReadFile(cachePath); err == nil && isValidPDF(data) && int64(len(data)) <= cfg.MaxPDFSizeBytes {
			return &SelectedPDF{
				Filename: paper.Filename,
				Title:    paper.Title,
				PaperID:  paper.ID,
				Source:   "cache",
				Data:     data,
			}, nil
		}

		// Download using Chrome UA from primary URL then mirror URL.
		urls := []string{paper.PDFURL}
		if paper.MirrorPDFURL != "" && paper.MirrorPDFURL != paper.PDFURL {
			urls = append(urls, paper.MirrorPDFURL)
		}
		for _, dlURL := range urls {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			dlCtx, dlCancel := context.WithTimeout(ctx, arxivDownloadTimeout)
			resp, dlErr := c.http.DoTransfer(dlCtx, http.MethodGet, dlURL, nil, map[string]string{
				"Accept":  "application/pdf,*/*;q=0.8",
				"Referer": "https://arxiv.org/abs/" + paper.ID,
			})
			dlCancel()
			if dlErr != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				lastErr = dlErr
				continue
			}
			if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, dlURL)
				continue
			}
			if !isValidPDF(resp.Body) {
				lastErr = fmt.Errorf("non-PDF response from %s", dlURL)
				continue
			}
			if int64(len(resp.Body)) > cfg.MaxPDFSizeBytes {
				lastErr = fmt.Errorf("PDF too large (%d bytes) from %s", len(resp.Body), dlURL)
				continue
			}
			// Best-effort atomic cache write
			tmp := cachePath + ".tmp"
			if os.WriteFile(tmp, resp.Body, 0o644) == nil {
				_ = os.Rename(tmp, cachePath)
			}
			return &SelectedPDF{
				Filename: paper.Filename,
				Title:    paper.Title,
				PaperID:  paper.ID,
				Source:   "arxiv_download",
				Data:     resp.Body,
			}, nil
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(cachedFiles) > 0 {
		if picked := loadCachedPaperPDF(cachedFiles, paperByFilename, "cache_fallback"); picked != nil {
			return picked, nil
		}
	}

	// Offline/air-gapped fallback: construct a valid minimal PDF based on a catalog paper's title & abstract.
	paper := shuffled[0]
	fallbackData := buildMinimalScientificPDF(paper)
	return &SelectedPDF{
		Filename: paper.Filename,
		Title:    paper.Title + " (offline fallback)",
		PaperID:  paper.ID,
		Source:   fmt.Sprintf("synthetic_fallback (%v)", lastErr),
		Data:     fallbackData,
	}, nil
}

func loadCachedPaperPDF(files []string, byFilename map[string]prompts.Paper, source string) *SelectedPDF {
	if len(files) == 0 {
		return nil
	}
	start := secureRandInt(len(files))
	for i := 0; i < len(files); i++ {
		p := files[(start+i)%len(files)]
		data, err := os.ReadFile(p)
		if err != nil || !isValidPDF(data) {
			continue
		}
		name := filepath.Base(p)
		if meta, ok := byFilename[name]; ok {
			return &SelectedPDF{
				Filename: name,
				Title:    meta.Title,
				PaperID:  meta.ID,
				Source:   source,
				Data:     data,
			}
		}
		return &SelectedPDF{
			Filename: name,
			Title:    strings.TrimSuffix(name, filepath.Ext(name)),
			PaperID:  "cache:" + name,
			Source:   source,
			Data:     data,
		}
	}
	return nil
}

// CountCachedPDFs returns counts of cached arXiv PDFs and user custom PDFs.
func (c *Client) CountCachedPDFs() (cached int, custom int) {
	cfg := c.cfg.Get()
	cached = len(listLocalPDFs(cfg.PDFCacheDir, cfg.MaxPDFSizeBytes))
	custom = len(listLocalPDFs(cfg.LocalPDFDir, cfg.MaxPDFSizeBytes))
	return cached, custom
}

func listLocalPDFs(dir string, maxSize int64) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() < 100 || (maxSize > 0 && info.Size() > maxSize) {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

func isValidPDF(data []byte) bool {
	return len(data) > 64 && bytes.HasPrefix(data, []byte("%PDF-"))
}

func secureRandInt(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// buildMinimalScientificPDF produces a valid single-page PDF 1.4 containing ASCII paper metadata.
func buildMinimalScientificPDF(paper prompts.Paper) []byte {
	esc := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if r >= 32 && r <= 126 {
				if r == '(' || r == ')' || r == '\\' {
					b.WriteByte('\\')
				}
				b.WriteRune(r)
			} else {
				b.WriteByte(' ')
			}
		}
		return b.String()
	}
	title := esc(truncateText(paper.Title, 80))
	abs := esc(truncateText(paper.AbstractSnippet, 140))
	stream := fmt.Sprintf("BT\n/F1 14 Tf\n50 740 Td\n(%s) Tj\n/F1 10 Tf\n0 -24 Td\n(arXiv: %s | Category: %s) Tj\n0 -24 Td\n(%s) Tj\nET\n",
		title, esc(paper.ID), esc(paper.Category), abs)

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, 6)
	writeObj := func(obj string) {
		offsets = append(offsets, buf.Len())
		buf.WriteString(obj)
	}
	writeObj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	writeObj("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	writeObj("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")
	writeObj(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(stream), stream))
	writeObj("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	xrefPos := buf.Len()
	buf.WriteString(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1))
	for _, off := range offsets {
		buf.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}
	buf.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xrefPos))
	return buf.Bytes()
}
