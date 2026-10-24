package app

import (
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// routes6 — метаданные трека и обложка, README проекта, проверка связи с сервером.
func (s *Server) routes6(api func(string, func(r *http.Request) (any, error))) {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	body := func(r *http.Request) map[string]any { m := map[string]any{}; decode(r, &m); return m }
	jobAPI := func(path, title string, fn func(b map[string]any, j *Job) (any, error)) {
		api(path, func(r *http.Request) (any, error) {
			b := body(r)
			j := s.startJobOpt(title, str(b, "path"), false, func(j *Job) (any, error) { return fn(b, j) })
			return map[string]string{"id": j.ID, "job": j.ID, "title": title}, nil
		})
	}

	// метаданные проекта: GET ?path= — прочитать; POST {path, meta, flp} — сохранить (название и артист — и в .flp)
	api("/api/project/meta", func(r *http.Request) (any, error) {
		if r.Method == "GET" {
			p, err := s.project(r.URL.Query().Get("path"), nil)
			if err != nil {
				return nil, err
			}
			res := map[string]any{"meta": LoadTrackMeta(p.Dir)}
			if c := coverPath(p.Dir); c != "" {
				if b, err := os.ReadFile(c); err == nil {
					mime := "image/jpeg"
					if strings.HasSuffix(strings.ToLower(c), ".png") {
						mime = "image/png"
					}
					res["cover"] = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
					res["coverName"] = metaDir + "/" + filepath.Base(c)
				}
			}
			return res, nil
		}
		var req struct {
			Path string    `json:"path"`
			Meta TrackMeta `json:"meta"`
			FLP  bool      `json:"flp"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		p, err := s.project(req.Path, nil)
		if err != nil {
			return nil, err
		}
		if err := SaveTrackMeta(p.Dir, req.Meta); err != nil {
			return nil, err
		}
		res := map[string]any{"ok": true}
		if req.FLP {
			if _, err := p.SetFLTitle(req.Meta.Title); err != nil {
				res["flError"] = err.Error()
			} else if _, err := p.SetFLArtist(req.Meta.Artist); err != nil {
				res["flError"] = err.Error()
			}
			if hasCyrillic(req.Meta.Title + req.Meta.Artist) {
				res["latin"] = strings.TrimSpace(toLatin(req.Meta.Title) + " — " + toLatin(req.Meta.Artist))
			}
		}
		return res, nil
	})
	api("/api/project/meta/cover", func(r *http.Request) (any, error) {
		b := body(r)
		p, err := s.project(str(b, "path"), nil)
		if err != nil {
			return nil, err
		}
		if str(b, "action") == "remove" {
			return "ok", SetCover(p.Dir, "")
		}
		src := str(b, "file")
		if src == "" {
			src, _ = PickAnyFile()
		}
		if src == "" {
			return map[string]any{"ok": false}, nil
		}
		return map[string]any{"ok": true}, SetCover(p.Dir, src)
	})
	// подписать рендеры проекта сейчас (все, включая старые)
	jobAPI("/api/project/meta/tag", "Записываю метаданные в рендеры", func(b map[string]any, j *Job) (any, error) {
		p, err := s.project(str(b, "path"), j)
		if err != nil {
			return nil, err
		}
		if f := str(b, "file"); f != "" {
			m := LoadTrackMeta(p.Dir)
			var cover *coverImg
			if !s.cfg.Meta.NoCover {
				cover = loadCover(p.Dir)
			}
			if err := TagFile(f, m, cover); err != nil {
				return nil, err
			}
			j.Logf("✓ Записано в %s", filepath.Base(f))
			return "ok", nil
		}
		done, err := TagRenders(s.cfg, p, true)
		for _, d := range done {
			j.Logf("✓ %s", d)
		}
		if len(done) == 0 && err == nil {
			j.Logf("Рендеров в папке проекта не нашлось (WAV/MP3/FLAC вне папок Audio и Backup, которые проект не использует)")
		}
		return len(done), err
	})
	api("/api/project/renders", func(r *http.Request) (any, error) {
		p, err := s.project(r.URL.Query().Get("path"), nil)
		if err != nil {
			return nil, err
		}
		l := renderCandidates(p, s.cfg.Meta.Extensions())
		if l == nil {
			l = []string{}
		}
		return l, nil
	})
	// README.md проекта — собрать сейчас
	api("/api/project/readme", func(r *http.Request) (any, error) {
		p, err := s.project(str(body(r), "path"), nil)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(p.Dir, "README.md")
		if old, err := os.ReadFile(path); err == nil && !strings.Contains(string(old), readmeMark) {
			return nil, errors.New("README.md в проекте написан вручную — FruityHub его не трогает")
		}
		p.WriteReadme(p.Analyze())
		return "ok", nil
	})
	// проверка связи с сервером FruityHub: отвечает ли, сколько миллисекунд, узнаёт ли меня
	api("/api/server/test", func(r *http.Request) (any, error) {
		base := s.cfg.ServerURL()
		if base == "" {
			return map[string]any{"ok": false, "error": "адрес сервера не указан"}, nil
		}
		t0 := time.Now()
		cl := &http.Client{Timeout: 15 * time.Second}
		resp, err := cl.Get(base + "/api/ping")
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error(), "server": base}, nil
		}
		resp.Body.Close()
		ms := time.Since(t0).Milliseconds()
		if resp.StatusCode != 200 {
			return map[string]any{"ok": false, "error": "сервер ответил " + resp.Status, "server": base}, nil
		}
		res := map[string]any{"ok": true, "ms": ms, "server": base}
		if s.cfg.Hub().Registered() {
			if _, err := s.cfg.Hub().Me(); err != nil {
				res["ok"] = false
				res["error"] = "сервер отвечает, но не узнаёт этот компьютер: " + err.Error()
			}
		}
		return res, nil
	})
}
