package app

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The "meta" branch (flsync-meta) holds comments, project status and plugin
// requests. It is written with git plumbing and a temporary index, so it never
// touches the working folder: posting a comment is instant and cannot clash
// with an open FL Studio project.

const (
	metaBranch = "flsync-meta"
	metaRef    = "refs/remotes/origin/" + metaBranch
)

var ErrNoRemote = errors.New("у проекта не указан сервер (remote)")

// Fetch updates all remote branches (project + meta) without merging.
func (g *Git) Fetch() error {
	if g.Remote() == "" {
		return ErrNoRemote
	}
	_, err := g.RunQuiet("fetch", "--no-tags", "--prune", "origin")
	return err
}

func (g *Git) metaHead() string {
	h, err := g.RunQuiet("rev-parse", "--verify", "--quiet", metaRef)
	if err != nil {
		return ""
	}
	return h
}

var (
	metaCacheMu sync.Mutex
	metaCache   = map[string]map[string][]byte{} // commit -> files
)

// MetaFiles returns files of the meta branch under prefix (as last fetched).
func (g *Git) MetaFiles(prefix string) map[string][]byte {
	head := g.metaHead()
	res := map[string][]byte{}
	if head == "" {
		return res
	}
	metaCacheMu.Lock()
	all, ok := metaCache[head]
	metaCacheMu.Unlock()
	if !ok {
		all = map[string][]byte{}
		out, err := g.RunQuiet("ls-tree", "-r", "-z", "--full-tree", head)
		if err != nil {
			return res
		}
		var shas, paths []string
		for _, rec := range strings.Split(out, "\x00") {
			tab := strings.IndexByte(rec, '\t')
			if tab < 0 {
				continue
			}
			f := strings.Fields(rec[:tab])
			if len(f) == 3 && f[1] == "blob" {
				shas = append(shas, f[2])
				paths = append(paths, rec[tab+1:])
			}
		}
		if len(shas) > 0 {
			raw, err := g.RunInput([]byte(strings.Join(shas, "\n")+"\n"), "cat-file", "--batch")
			if err != nil {
				return res
			}
			for i := 0; i < len(shas) && len(raw) > 0; i++ {
				nl := bytes.IndexByte(raw, '\n')
				if nl < 0 {
					break
				}
				hdr := strings.Fields(string(raw[:nl]))
				raw = raw[nl+1:]
				if len(hdr) < 3 {
					break
				}
				n, _ := strconv.Atoi(hdr[2])
				if n > len(raw) {
					break
				}
				all[paths[i]] = raw[:n]
				raw = raw[n:]
				if len(raw) > 0 && raw[0] == '\n' {
					raw = raw[1:]
				}
			}
		}
		metaCacheMu.Lock()
		metaCache[head] = all
		metaCacheMu.Unlock()
	}
	for p, b := range all {
		if strings.HasPrefix(p, prefix) {
			res[p] = b
		}
	}
	return res
}

// MetaWrite atomically adds/replaces (data != nil) or removes (nil) files on
// the meta branch and pushes it. Retries if the other side pushed meanwhile.
func (g *Git) MetaWrite(files map[string][]byte, msg string) error {
	if g.Remote() == "" {
		return ErrNoRemote
	}
	idx := filepath.Join(g.Dir, ".git", "flsync-meta.index")
	for attempt := 0; attempt < 5; attempt++ {
		if err := g.Fetch(); err != nil {
			return err
		}
		if !g.RemoteBranchExists(g.Branch()) {
			return errors.New("сначала сделай первую отправку проекта")
		}
		parent := g.metaHead()
		ig := &Git{Dir: g.Dir, Cfg: g.Cfg, Env: []string{"GIT_INDEX_FILE=" + idx}}
		var err error
		if parent != "" {
			_, err = ig.RunQuiet("read-tree", parent)
		} else {
			_, err = ig.RunQuiet("read-tree", "--empty")
		}
		if err != nil {
			return err
		}
		var info strings.Builder
		for path, data := range files {
			if data == nil {
				info.WriteString("0 0000000000000000000000000000000000000000\t" + path + "\n")
				continue
			}
			sha, err := ig.RunInput(data, "hash-object", "-w", "--stdin")
			if err != nil {
				return err
			}
			info.WriteString("100644 " + strings.TrimSpace(string(sha)) + "\t" + path + "\n")
		}
		if _, err := ig.RunInput([]byte(info.String()), "update-index", "--index-info"); err != nil {
			return err
		}
		tree, err := ig.RunQuiet("write-tree")
		if err != nil {
			return err
		}
		args := []string{"commit-tree", tree, "-m", msg}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		commit, err := ig.RunQuiet(args...)
		if err != nil {
			return err
		}
		_, err = g.RunQuiet("push", "--no-verify", "origin", commit+":refs/heads/"+metaBranch)
		if err == nil {
			g.RunQuiet("update-ref", metaRef, commit)
			return nil
		}
		if !isPushRejected(err) {
			return err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("не удалось записать: сервер всё время меняется, попробуй ещё раз")
}

func newID(user string) string {
	b := make([]byte, 3)
	rand.Read(b)
	return fmt.Sprintf("%d-%s-%s", time.Now().UnixMilli(), safeID(user), hex.EncodeToString(b))
}

func safeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r > 127 {
			b.WriteString(translit(r))
		}
	}
	if b.Len() == 0 {
		return "user"
	}
	return b.String()
}

// ---------- comments & status ----------

type Comment struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Time    int64  `json:"time"`
	Updated int64  `json:"updated"`
	Kind    string `json:"kind"` // note | todo
	Text    string `json:"text"`
	Done    bool   `json:"done,omitempty"`
	DoneBy  string `json:"doneBy,omitempty"`
}

type Status struct {
	Stage string `json:"stage"`
	Text  string `json:"text"`
	By    string `json:"by"`
	Time  int64  `json:"time"`
}

func (p *Project) Comments() ([]Comment, *Status) {
	g := p.git()
	var cs []Comment
	for _, b := range g.MetaFiles("comments/") {
		var c Comment
		if json.Unmarshal(b, &c) == nil && c.ID != "" {
			cs = append(cs, c)
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].ID > cs[j].ID })
	var st *Status
	if b, ok := g.MetaFiles("status.json")["status.json"]; ok {
		st = &Status{}
		json.Unmarshal(b, st)
	}
	return cs, st
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return append(b, '\n')
}

func (p *Project) AddComment(kind, text string) (*Comment, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("пустой комментарий")
	}
	if kind != "todo" {
		kind = "note"
	}
	now := time.Now().Unix()
	c := &Comment{ID: newID(p.Cfg.MachineID()), Author: p.Cfg.MachineID(), Time: now, Updated: now, Kind: kind, Text: text}
	short := text
	if r := []rune(short); len(r) > 50 {
		short = string(r[:50]) + "…"
	}
	return c, p.git().MetaWrite(map[string][]byte{"comments/" + c.ID + ".json": mustJSON(c)}, ""+c.Author+": "+short)
}

func (p *Project) UpdateComment(id string, done *bool, del bool) error {
	g := p.git()
	g.Fetch()
	path := "comments/" + id + ".json"
	b, ok := g.MetaFiles(path)[path]
	if !ok {
		return errors.New("комментарий не найден")
	}
	var c Comment
	json.Unmarshal(b, &c)
	me := p.Cfg.MachineID()
	if del {
		if c.Author != me {
			return errors.New("удалять можно только свои комментарии")
		}
		return g.MetaWrite(map[string][]byte{path: nil}, "комментарий удалён")
	}
	if done != nil {
		c.Done = *done
		c.DoneBy = ""
		if c.Done {
			c.DoneBy = me
		}
	}
	c.Updated = time.Now().Unix()
	msg := "↺ задача снова открыта"
	if c.Done {
		msg = "✓ " + me + " выполнил задачу"
	}
	return g.MetaWrite(map[string][]byte{path: mustJSON(c)}, msg)
}

func (p *Project) SetStatus(stage, text string) error {
	st := Status{Stage: strings.TrimSpace(stage), Text: strings.TrimSpace(text), By: p.Cfg.MachineID(), Time: time.Now().Unix()}
	return p.git().MetaWrite(map[string][]byte{"status.json": mustJSON(st)}, "статус: "+st.Stage)
}

func translit(r rune) string {
	m := map[rune]string{'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "y",
		'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h",
		'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ы': "y", 'э': "e", 'ю': "yu", 'я': "ya"}
	lr := []rune(strings.ToLower(string(r)))[0]
	if s, ok := m[lr]; ok {
		return s
	}
	return ""
}
