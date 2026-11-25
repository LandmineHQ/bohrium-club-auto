package recon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestReconPullAndDiff(t *testing.T) {
	var version atomic.Int32
	version.Store(1)

	var reqSeq atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			n := reqSeq.Add(1)
			_, _ = w.Write([]byte(`<!doctype html><html><head><script src="//o.alicdn.com/j.js" clientSignal="ephemeral-` + string(rune('0'+n)) + `"></script><script type="module" src="/plaza/daily-task-20261119/app.js"></script></head><body>ok</body></html>`))
		case "/plaza/daily-task-20261119/app.js":
			w.Header().Set("Content-Type", "application/javascript")
			if version.Load() == 1 {
				_, _ = w.Write([]byte(`fetch("/api/plaza/tasks", {headers:{"X-Plaza-Daily-Task-Gateway":"production"}}); fetch("/api/plaza/tasks/claim");`))
			} else {
				_, _ = w.Write([]byte(`fetch("/api/plaza/tasks/v2", {headers:{"X-Plaza-Daily-Task-Gateway":"production"}});`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "recon-test")
	opts := Options{
		TargetURLs: []string{srv.URL + "/"},
		OutDir:     outDir,
		MaxAssets:  20,
		DelayMs:    0,
	}

	manifest, err := TakeSnapshot(context.Background(), opts, true)
	if err != nil {
		t.Fatalf("TakeSnapshot(write=true): %v", err)
	}
	if !manifest.Markers.PlazaTasksEndpoint || !manifest.Markers.PlazaClaimEndpoint || !manifest.Markers.GatewayHeader {
		t.Fatalf("expected markers detected, got %+v", manifest.Markers)
	}

	// Same upstream -> Same == true
	diff1, err := CompareLive(context.Background(), opts)
	if err != nil {
		t.Fatalf("CompareLive(same): %v", err)
	}
	if !diff1.Same {
		t.Fatalf("expected Same=true, got %+v", diff1)
	}

	// Mutate upstream bundle -> Same == false, 1 changed asset, marker change detected
	version.Store(2)
	diff2, err := CompareLive(context.Background(), opts)
	if err != nil {
		t.Fatalf("CompareLive(changed): %v", err)
	}
	if diff2.Same || len(diff2.Changed) != 1 || len(diff2.MarkerChanges) == 0 {
		t.Fatalf("expected drift detected with changed asset & marker diff, got %+v", diff2)
	}
}
