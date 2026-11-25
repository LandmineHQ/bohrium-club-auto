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

	"bohrium-club-auto/internal/prompts"
)

// SelectedPDF holds the binary payload and metadata for Task 3 (Literature Reading).
type SelectedPDF struct {
	Filename string
	Title    string
	PaperID  string
	Source   string // "custom_local" | "cache" | "arxiv_download" | "synthetic_fallback"
	Data     []byte
}

// AcquireRandomPaperPDF selects a random scientific PDF for Task 3:
// 1. If user placed custom .pdf files in data/custom_pdfs/, randomly includes them.
// 2. Otherwise picks randomly from the embedded 100-paper arXiv catalog, reading from data/pdfs/ cache
//    or downloading via Chrome HTTP client (with mirror fallback and up to 5 catalog retries).
// 3. Only if all external downloads fail (e.g., offline environment), generates a minimal valid PDF.
func (c *Client) AcquireRandomPaperPDF(ctx context.Context) (*SelectedPDF, error) {
	cfg := c.cfg.Get()

	// Check optional user-supplied local PDFs first (if any exist, 30% chance or primary if desired;
	// if user puts PDFs in custom_pdfs, pick randomly among them).
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

	// Try up to 6 distinct random papers from our 100-paper catalog.
	maxAttempts := 6
	if len(shuffled) < maxAttempts {
		maxAttempts = len(shuffled)
	}
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
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
			resp, dlErr := c.http.DoRaw(ctx, http.MethodGet, dlURL, nil, map[string]string{
				"Accept":  "application/pdf,*/*;q=0.8",
				"Referer": "https://arxiv.org/abs/" + paper.ID,
			})
			if dlErr != nil {
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
