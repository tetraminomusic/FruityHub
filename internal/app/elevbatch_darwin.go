//go:build darwin

package app

import (
	"path/filepath"
	"strings"
	"time"
)

func runElevBatch(ops []elevOp, log string) error {
	var cmds []string
	fail := func(cmd, msg string) string {
		return "{ " + cmd + " ; } 2>>" + shq(log) + " || { echo " + shq(msg) + " >> " + shq(log) + "; f=1; }"
	}
	cmds = append(cmds, "f=0")
	for _, o := range ops {
		switch o.Kind {
		case "replace":
			bak := filepath.Join(ConfigDir(), "plugin-backups", time.Now().Format("20060102-150405"))
			cmds = append(cmds, fail("mkdir -p "+shq(filepath.Dir(o.B))+" && { [ ! -e "+shq(o.B)+" ] || { mkdir -p "+shq(bak)+" && mv "+shq(o.B)+" "+shq(bak)+"/; }; } && /usr/bin/ditto "+shq(o.A)+" "+shq(o.B), "Плагин → "+o.B))
		case "moveaway":
			cmds = append(cmds, fail("mkdir -p "+shq(o.B)+" && mv "+shq(o.A)+" "+shq(o.B)+"/ && { rmdir "+shq(filepath.Dir(o.A))+" 2>/dev/null; true; }", "Старая копия "+o.A))
		case "merge", "mergeover":
			ign := "--ignore-existing "
			if o.Kind == "mergeover" {
				ign = ""
			}
			cmds = append(cmds, fail("mkdir -p "+shq(o.B)+" && /usr/bin/rsync -a "+ign+shq(strings.TrimRight(o.A, "/")+"/")+" "+shq(strings.TrimRight(o.B, "/")+"/"), "Данные → "+o.B))
		}
	}
	cmds = append(cmds, "exit $f")
	return asAdmin(strings.Join(cmds, "; "))
}
