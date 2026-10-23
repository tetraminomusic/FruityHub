package app

import (
	"fmt"
	"strings"
)

// missingPluginRequests — какие плагины проекта у меня не стоят и у кого из участников их взять
// (лучше у того, у кого та же система). Ключ — ник участника.
func missingPluginRequests(p *Project) map[string][]ReqItem {
	a := p.Analyze()
	machines := p.Machines()
	me := p.myMachine(a)
	found := false
	for i := range machines {
		if machines[i].User == me.User {
			machines[i], found = me, true
		}
	}
	if !found {
		machines = append(machines, me)
	}
	out := map[string][]ReqItem{}
	for _, r := range p.PluginReport(a, machines) {
		if r.Format == "FL" {
			continue
		}
		mine := false
		var donors []MachineStatus
		for _, ms := range r.Machines {
			switch {
			case ms.User == me.User:
				mine = ms.State == "ok"
			case ms.State == "ok":
				donors = append(donors, ms)
			}
		}
		if mine || len(donors) == 0 {
			continue
		}
		pick := donors[0]
		for _, d := range donors {
			if d.OS == OSName() {
				pick = d
				break
			}
		}
		out[pick.User] = append(out[pick.User], ReqItem{Plugin: r.Name, Format: r.Format, Vendor: r.Vendor})
	}
	return out
}

// autoRequestMissing: включено «запрашивать недостающие плагины» — после скачивания проекта сразу просим их у участников.
func (s *Server) autoRequestMissing(j *Job, dir string) {
	if !s.cfg.AutoFetchPlugins || dir == "" || !s.cfg.Hub().Registered() {
		return
	}
	p := &Project{Dir: dir, Cfg: s.cfg, Job: j}
	want := missingPluginRequests(p)
	key := p.requestKey()
	for who := range want {
		for i := range want[who] {
			want[who][i].Project = key
		}
	}
	if len(want) == 0 {
		j.Logf("✓ Все плагины проекта у тебя есть")
		return
	}
	for who, items := range want {
		rs, err := s.cfg.Hub().RequestPlugins(who, items)
		if err != nil {
			j.Logf("Внимание: не получилось запросить плагины у %s: %s", who, err.Error())
			continue
		}
		var names []string
		for _, it := range items {
			names = append(names, it.Plugin)
		}
		j.Logf("Запросил у %s недостающие плагины (%d): %s", who, len(rs), strings.Join(names, ", "))
		LogActivity(dir, "info", fmt.Sprintf("Запрошены недостающие плагины у %s: %s", who, strings.Join(names, ", ")))
	}
	go s.cfg.Hub().Inbox()
}
