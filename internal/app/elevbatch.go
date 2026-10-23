package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// elevBatch собирает всё, что при установке плагина требует прав администратора (плагин в Program Files,
// данные в Program Files/ProgramData, пути в HKLM, уборка старой копии), чтобы система спросила разрешение
// один раз. Если что-то не получилось — в ошибке будет точная причина по каждому шагу.
type elevBatch struct {
	ops []elevOp
}

type elevOp struct {
	Kind string // replace, moveaway, merge, reg
	A, B string // replace/merge: откуда, куда; moveaway: что, куда (папка резервной копии); reg: строка PowerShell, ключ
}

func (b *elevBatch) Replace(src, dst string)   { b.ops = append(b.ops, elevOp{"replace", src, dst}) }
func (b *elevBatch) MoveAway(p, bak string)    { b.ops = append(b.ops, elevOp{"moveaway", p, bak}) }
func (b *elevBatch) Merge(src, dst string)     { b.ops = append(b.ops, elevOp{"merge", src, dst}) }
func (b *elevBatch) MergeOver(src, dst string) { b.ops = append(b.ops, elevOp{"mergeover", src, dst}) }
func (b *elevBatch) Reg(line, key string)      { b.ops = append(b.ops, elevOp{"reg", line, key}) }
func (b *elevBatch) Empty() bool               { return b == nil || len(b.ops) == 0 }

func (b *elevBatch) has(kind string) bool {
	for _, o := range b.ops {
		if o.Kind == kind {
			return true
		}
	}
	return false
}

// Run выполняет всё одним запросом прав. Возвращает ошибки по шагам (пусто — всё прошло).
func (b *elevBatch) Run(job *Job) []string {
	if b.Empty() {
		return nil
	}
	if job != nil {
		job.Logf("Нужны права администратора (Program Files / системные папки) — %s спросит разрешение один раз", OSName())
	}
	log := filepath.Join(os.TempDir(), "fruityhub-admin-"+time.Now().Format("150405.000")+".log")
	defer os.Remove(log)
	err := runElevBatch(b.ops, log)
	raw, _ := os.ReadFile(log)
	var fails []string
	for _, l := range strings.Split(strings.ReplaceAll(strings.TrimPrefix(string(raw), "\ufeff"), "\r", ""), "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(l, "\ufeff")); l != "" {
			fails = append(fails, l)
		}
	}
	if err != nil && len(fails) == 0 {
		fails = append(fails, err.Error())
	}
	for _, f := range fails {
		DevLog("error", 0, true, "права администратора: %s", f)
	}
	return fails
}

// ---------- данные, которые не удалось положить: можно повторить без новой передачи ----------

type pendingMeta struct {
	Plugin string     `json:"plugin"`
	BinDir string     `json:"binDir"`
	Home   string     `json:"home"`
	Reg    []RegValue `json:"reg"`
	Over   bool       `json:"over,omitempty"`
	At     int64      `json:"at"`
}

func pendingDir(id string) string { return filepath.Join(ConfigDir(), "pending-data", id) }

// savePending сохраняет полученные данные плагина, чтобы положить их ещё раз (кнопка в отчёте).
func savePending(plugin, dataStageDir, binDir, home string, reg []RegValue, over bool) string {
	id := time.Now().Format("20060102-150405.000")
	dir := pendingDir(id)
	os.MkdirAll(dir, 0o755)
	if dataStageDir != "" && dirExists(dataStageDir) {
		if err := moveAll(dataStageDir, filepath.Join(dir, "data")); err != nil {
			copyTree(dataStageDir, filepath.Join(dir, "data"))
		}
	}
	writeJSON(filepath.Join(dir, "meta.json"), pendingMeta{Plugin: plugin, BinDir: binDir, Home: home, Reg: reg, At: time.Now().Unix(), Over: over})
	return id
}

// RetryPendingData — ещё раз положить данные и пути реестра, которые не легли при установке.
func RetryPendingData(job *Job, id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return errors.New("неверный id")
	}
	dir := pendingDir(id)
	var m pendingMeta
	if err := readJSON(filepath.Join(dir, "meta.json"), &m); err != nil {
		return errors.New("эти данные уже разложены или удалены — передай плагин ещё раз")
	}
	batch := &elevBatch{}
	var done, problems []string
	if ds := filepath.Join(dir, "data"); dirExists(ds) {
		done, problems = installData(job, ds, m.BinDir, batch, m.Over)
	}
	rd, rp := regApply(job, m.Reg, m.Home, batch, m.Over)
	done, problems = append(done, rd...), append(problems, rp...)
	if fails := batch.Run(job); len(fails) > 0 {
		problems = append(problems, fails...)
	}
	for _, d := range done {
		job.Logf("✓ %s", d)
	}
	if len(problems) > 0 {
		return fmt.Errorf("не получилось: %s", strings.Join(problems, "; "))
	}
	os.RemoveAll(dir)
	job.Logf("✓ Данные %s разложены. Перезапусти FL Studio", m.Plugin)
	return nil
}

var _ = json.Marshal
