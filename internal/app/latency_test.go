package app

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"flsync/internal/flp"
	"flsync/web"
)

// These fixtures never load a real config or enumerate installed plugins.
func latencyServer(t testing.TB, projects int) *Server {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ProgramFiles", filepath.Join(root, "programs"))
	cfg := &Config{UserName: "Benchmark", UserID: "fixture-user", GuideSeen: true, NoLAN: true, Server: "http://127.0.0.1:1"}
	s := &Server{cfg: cfg, token: "fixture-token", jobs: map[string]*Job{}, notes: map[string]*ProjectNotes{}, quit: make(chan struct{})}
	for i := 0; i < projects; i++ {
		dir := filepath.Join(root, fmt.Sprintf("Project-%02d", i))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		f, err := flp.Parse(synthFLP("kick.wav"))
		if err != nil {
			t.Fatal(err)
		}
		f.SetArtist("Fixture Artist")
		state := make([]byte, 1<<20)
		rand.New(rand.NewSource(42)).Read(state)
		f.Events = append(f.Events, flp.Event{ID: 213, Data: state})
		if err := os.WriteFile(filepath.Join(dir, "Track.flp"), f.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		cfg.Projects = append(cfg.Projects, LocalProject{Path: dir, Name: fmt.Sprintf("Project %02d", i)})
	}
	scanMu.Lock()
	oldScan, oldTime := scanCache, scanTime
	scanCache, scanTime = []InstalledPlugin{}, time.Now()
	scanMu.Unlock()
	// Random state bytes can resemble an embedded sample name. Never let a
	// fixture start indexing system/shared sample libraries to resolve it.
	sampleIndex.start.Do(func() {
		sampleIndex.names = map[string]string{}
		close(sampleIndex.ready)
	})
	t.Cleanup(func() {
		scanMu.Lock()
		scanCache, scanTime = oldScan, oldTime
		scanMu.Unlock()
		flpCacheMu.Lock()
		if flpSaveTimer != nil {
			flpSaveTimer.Stop()
			flpSaveTimer = nil
		}
		for _, p := range cfg.Projects {
			delete(flpCache, filepath.Join(p.Path, "Track.flp"))
		}
		flpCacheMu.Unlock()
	})
	return s
}

func latencyRequest(t testing.TB, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "http://127.0.0.1:0"+target, nil)
	r.Header.Set("X-FLSync", "fixture-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", target, w.Code, w.Body.String())
	}
	return w
}

func BenchmarkUILatency(b *testing.B) {
	for _, scenario := range []string{"ListQuick20", "ListCold20", "ListWarm20", "ProjectCold", "ProjectWarm"} {
		b.Run(scenario, func(b *testing.B) {
			count := 20
			if scenario == "ProjectCold" || scenario == "ProjectWarm" {
				count = 1
			}
			s := latencyServer(b, count)
			h := s.Handler(0)
			target := "/api/state"
			if scenario == "ListQuick20" {
				target += "?quick=1"
			} else if count == 1 {
				target = "/api/project?path=" + url.QueryEscape(s.cfg.Projects[0].Path)
			}
			latencyRequest(b, h, target)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if scenario == "ListCold20" || scenario == "ProjectCold" {
					flpCacheMu.Lock()
					for _, p := range s.cfg.Projects {
						delete(flpCache, filepath.Join(p.Path, "Track.flp"))
					}
					flpCacheMu.Unlock()
				}
				latencyRequest(b, h, target)
			}
		})
	}
}

func TestUIProjectMetadataAndAnalysis(t *testing.T) {
	s := latencyServer(t, 2)
	path := filepath.Join(s.cfg.Projects[0].Path, "Track.flp")
	f, err := flp.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.SetTitle("Fixture Track")
	tempo, selection := make([]byte, 4), make([]byte, 8)
	binary.LittleEndian.PutUint32(tempo, 140000)
	binary.LittleEndian.PutUint32(selection[4:], 1536)
	f.Events = append(f.Events, flp.Event{ID: 156, Data: tempo}, flp.Event{ID: 217, Data: selection})
	if err := os.WriteFile(path, f.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	h := s.Handler(0)
	w := latencyRequest(t, h, "/api/state")
	var state struct {
		Projects []ProjectSummary `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Projects) != 2 || state.Projects[0].Artist != "Fixture Artist" || state.Projects[0].Length != 7 {
		t.Fatal(state)
	}
	flpCacheMu.Lock()
	partial := flpCache[path].MetadataOnly
	flpCacheMu.Unlock()
	if !partial {
		t.Fatal("list performed a full dependency scan")
	}
	summaryResponse := latencyRequest(t, h, "/api/project?path="+url.QueryEscape(s.cfg.Projects[0].Path))
	var summary ProjectSummary
	if err := json.Unmarshal(summaryResponse.Body.Bytes(), &summary); err != nil || summary.Tempo != "140" {
		t.Fatalf("summary tempo: %+v %v", summary, err)
	}
	w = latencyRequest(t, h, "/api/project/analysis?path="+url.QueryEscape(s.cfg.Projects[0].Path))
	var analysis struct {
		Analysis Analysis `json:"analysis"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &analysis); err != nil {
		t.Fatal(err)
	}
	if len(analysis.Analysis.FLPs) != 1 || len(analysis.Analysis.Samples) < 1 || analysis.Analysis.Samples[0].Name != "kick.wav" || len(analysis.Analysis.Plugins) != 2 {
		t.Fatal(analysis)
	}
	flpCacheMu.Lock()
	partial = flpCache[path].MetadataOnly
	flpCacheMu.Unlock()
	if partial {
		t.Fatal("analysis accepted incomplete cache")
	}
	if _, err := parseFLPMetadataCached(path); err != nil {
		t.Fatal(err)
	}
	flpCacheMu.Lock()
	partial = flpCache[path].MetadataOnly
	flpCacheMu.Unlock()
	if partial {
		t.Fatal("metadata downgraded a full scan")
	}
	metadata, err := parseFLPMetadataCached(path)
	if err != nil || metadata.Title != "Fixture Track" || metadata.Artist != "Fixture Artist" || metadata.Tempo != 140 || metadata.LengthBy != "selection" {
		t.Fatalf("metadata fields: %+v %v", metadata, err)
	}
}

func TestFLPMetadataCachePersistenceAndInvalidation(t *testing.T) {
	s := latencyServer(t, 1)
	path := filepath.Join(s.cfg.Projects[0].Path, "Track.flp")
	if _, err := parseFLPMetadataCached(path); err != nil {
		t.Fatal(err)
	}
	flpCacheMu.Lock()
	if flpSaveTimer != nil {
		flpSaveTimer.Stop()
		flpSaveTimer = nil
	}
	err := writeJSON(flpCachePath(), flpCache)
	flpCache = nil
	flpCacheMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	full, err := parseFLPCached(path)
	if err != nil || len(full.Plugins) != 2 || len(full.Samples) != 1 {
		t.Fatalf("persisted partial entry lost dependencies: %+v %v", full, err)
	}
	if err := os.WriteFile(path, synthFLP("changed-sample.wav"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, err := parseFLPMetadataCached(path)
	if err != nil || metadata.Artist != "" {
		t.Fatalf("stale artist: %+v %v", metadata, err)
	}
	full, err = parseFLPCached(path)
	if err != nil || len(full.Samples) != 1 || full.Samples[0] != "changed-sample.wav" {
		t.Fatalf("stale dependencies: %+v %v", full, err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseFLPMetadataCached(path); err == nil {
		t.Fatal("metadata accepted broken FLP")
	}
	if _, err := parseFLPCached(path); err == nil {
		t.Fatal("full analysis accepted cached broken FLP")
	}
}

func TestFLPMetadataConcurrentUpgrade(t *testing.T) {
	s := latencyServer(t, 1)
	path := filepath.Join(s.cfg.Projects[0].Path, "Track.flp")
	start := make(chan struct{})
	errors := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(full bool) {
			defer wg.Done()
			<-start
			info, err := parseFLPCachedMode(path, !full)
			if err == nil && (info.Artist != "Fixture Artist" || full && len(info.Plugins) != 2) {
				err = fmt.Errorf("incomplete result: full=%v plugins=%d artist=%q", full, len(info.Plugins), info.Artist)
			}
			errors <- err
		}(i%2 == 0)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	flpCacheMu.Lock()
	partial := flpCache[path].MetadataOnly
	flpCacheMu.Unlock()
	if partial {
		t.Fatal("concurrent metadata replaced a full result")
	}
}

// Opt-in loopback fixture for visual checks. No app pollers, LAN discovery,
// updater or real Hub are started; all settings are isolated by TestMain.
func TestUIFixture(t *testing.T) {
	if os.Getenv("FRUITYHUB_UI_FIXTURE") != "1" {
		t.Skip("manual UI fixture")
	}
	s := latencyServer(t, 20)
	s.cfg.DeviceToken, s.cfg.InstallDeclined = "synthetic-device", true
	s.index, s.Assets = web.Index, web.FS
	fixture := httptest.NewUnstartedServer(nil)
	h := s.Handler(fixture.Listener.Addr().(*net.TCPAddr).Port)
	fixture.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// These are external-service results, not part of this local UI check.
		if r.URL.Path == "/api/available" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
			return
		}
		h.ServeHTTP(w, r)
	})
	fixture.Start()
	defer fixture.Close()
	t.Log("UI_FIXTURE_URL=" + fixture.URL)
	select {
	case <-s.Quit():
	case <-time.After(8 * time.Minute):
		t.Fatal("UI fixture expired")
	}
}
