package app

import (
	"bytes"
	"encoding/binary"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flsync/internal/fakegh"
	"flsync/internal/flp"
	"flsync/internal/hub"
)

// startHub runs our server against the fake GitHub.
func startHub(t *testing.T, root string, gh *fakegh.Server) string {
	s, err := hub.New(hub.Options{DataDir: filepath.Join(root, "hub-data"), GitHubAPI: gh.Srv.URL, Code: "uzbek", StreamWait: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func useFakeGitHub(t *testing.T, root string) *fakegh.Server {
	gh := fakegh.New(root)
	t.Cleanup(gh.Close)
	gh.AddUser("tokA", "alogin")
	gh.AddUser("tokB", "blogin")
	ghAPIBase = gh.Srv.URL
	repoSlug = func(remote string) (string, string, bool) {
		parts := strings.Split(strings.TrimSuffix(filepath.ToSlash(remote), ".git"), "/")
		if len(parts) < 2 {
			return "", "", false
		}
		return parts[len(parts)-2], parts[len(parts)-1], true
	}
	return gh
}

// fakeFST builds a tiny FL plugin preset that loads the plugin at path.
func fakeFST(path string) []byte {
	var d bytes.Buffer
	binary.Write(&d, binary.LittleEndian, uint32(10))
	chunk := func(id uint32, v string) {
		binary.Write(&d, binary.LittleEndian, id)
		binary.Write(&d, binary.LittleEndian, uint64(len(v)))
		d.WriteString(v)
	}
	chunk(54, "Serum")
	chunk(55, path)
	chunk(56, "Xfer Records")
	f := &flp.File{Format: 0x30, Channels: 2, PPQ: 0x30, Events: []flp.Event{{ID: 199, Data: []byte("11.5.5\x00")}, {ID: 213, Data: d.Bytes()}}}
	return f.Bytes()
}

func TestServerCollab(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	homeA, homeB := filepath.Join(root, "A-home"), filepath.Join(root, "B-home")

	// A has Serum, and FL on A knows it (plugin database entries)
	plugDir := filepath.Join(root, "A-plugins")
	bundle := filepath.Join(plugDir, "Serum.vst3")
	os.MkdirAll(filepath.Join(bundle, "Contents", "Resources"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Contents", "Resources", "data.bin"), []byte("serum-binary"), 0o755)
	os.Symlink("Resources", filepath.Join(bundle, "Contents", "Current"))
	dbA := filepath.Join(homeA, "Documents", "Image-Line", "FL Studio", "Presets", "Plugin database")
	os.MkdirAll(filepath.Join(dbA, "Installed", "Generators", "VST3"), 0o755)
	os.WriteFile(filepath.Join(dbA, "Installed", "Generators", "VST3", "Serum.nfo"),
		[]byte("\ufefffileversion=2\nps_name=Serum\nps_file_name_0=Serum\nps_file_filename_0="+bundle+"\nps_file_date_0=1\nps_file_vendorname_0=Xfer Records\n"), 0o644)
	os.WriteFile(filepath.Join(dbA, "Installed", "Generators", "VST3", "Serum.fst"), fakeFST(bundle), 0o644)
	os.MkdirAll(filepath.Join(dbA, "Generators", "Synth classic"), 0o755)
	os.WriteFile(filepath.Join(dbA, "Generators", "Synth classic", "Serum.fst"), fakeFST(bundle), 0o644)
	os.MkdirAll(filepath.Join(homeB, "Documents", "Image-Line", "FL Studio", "Presets", "Plugin database", "Installed"), 0o755)

	// --- registration: nick + GitHub token checked by the server ---
	cfgA := &Config{Server: hubURL, ExtraPluginDirs: []string{plugDir}}
	cfgB := &Config{Server: hubURL}
	if _, err := cfgA.Hub().Register("tetramino", "wrong", "tokA"); err == nil {
		t.Fatal("wrong server code accepted")
	}
	_, err := cfgA.Hub().Register("tetramino", "uzbek", "tokA")
	must(t, err)
	_, err = cfgB.Hub().Register("friend", "uzbek", "tokB")
	must(t, err)
	if cfgA.GitHubLogin != "alogin" || cfgA.Token != "tokA" || cfgB.GitHubLogin != "blogin" {
		t.Fatalf("registration: %+v", cfgA)
	}
	if _, err := (&Config{Server: hubURL}).Hub().Register("FRIEND", "uzbek", "tokB"); err == nil || !strings.Contains(err.Error(), "занят") {
		t.Fatalf("nick must be unique: %v", err)
	}

	// --- A creates the project: a private repo on A's GitHub + invite by nick ---
	aDir := filepath.Join(root, "A", "Ночной дрифт")
	os.MkdirAll(filepath.Join(aDir, "Audio"), 0o755)
	os.WriteFile(filepath.Join(aDir, "Track.flp"), synthFLP(filepath.Join(aDir, "Audio", "vox.wav")), 0o644)
	os.WriteFile(filepath.Join(aDir, "Audio", "vox.wav"), []byte("RIFF-vocal"), 0o644)
	jA := &Job{}
	A := &Project{Dir: aDir, Cfg: cfgA, Job: jA}
	if _, err := A.CreateOnGitHub("Ночной дрифт", []string{"nobody"}); err == nil {
		t.Fatal("unknown nick accepted")
	}
	if _, err := A.CreateOnGitHub("Ночной дрифт", []string{"friend"}); err != nil {
		t.Fatal(err, dump(jA))
	}
	hp := cfgA.Hub().ProjectForRemote(A.git().Remote())
	if hp == nil || hp.Repo != "alogin/nochnoy-drift" || !hp.IsOwner {
		t.Fatalf("project: %+v remote %s", hp, A.git().Remote())
	}
	if !strings.Contains(A.git().Remote(), filepath.Join("github", "alogin")) {
		t.Fatal("the repo must be on A's own GitHub")
	}

	// --- B gets the invitation in the app, accepts, downloads ---
	in, err := cfgB.Hub().Inbox()
	must(t, err)
	if len(in.Invites) != 1 || in.Invites[0].From.Nick != "tetramino" || in.Invites[0].Repo != "alogin/nochnoy-drift" {
		t.Fatalf("B inbox: %+v", in.Invites)
	}
	acc, err := cfgB.Hub().AcceptInvite(in.Invites[0].ID)
	must(t, err)
	jB := &Job{}
	bDir, err := CloneServerProject(cfgB, jB, acc, filepath.Join(root, "B"))
	if err != nil {
		t.Fatal(err, dump(jB))
	}
	if cs := gh.Collaborators("alogin/nochnoy-drift"); len(cs) != 1 || cs[0] != "blogin" {
		t.Fatalf("GitHub invitation not accepted: %v", cs)
	}
	if filepath.Base(bDir) != "Ночной дрифт" {
		t.Fatalf("folder: %s", bDir)
	}
	if b, _ := os.ReadFile(filepath.Join(bDir, "Audio", "vox.wav")); string(b) != "RIFF-vocal" {
		t.Fatalf("audio: %q", b)
	}
	B := &Project{Dir: bDir, Cfg: cfgB, Job: jB}

	// comments and preview work for both
	_, err = A.AddComment("todo", "Нужен вокал")
	must(t, err)
	must(t, B.git().Fetch())
	if cs, _ := B.Comments(); len(cs) != 1 {
		t.Fatalf("comments: %+v", cs)
	}
	wav := filepath.Join(root, "mix.wav")
	os.WriteFile(wav, []byte("RIFF-mix"), 0o644)
	_, err = A.UploadPreview(wav)
	must(t, err)
	os.RemoveAll(filepath.Join(ConfigDir(), "cache"))
	pth, err := B.PreviewFile(jB)
	must(t, err)
	if b, _ := os.ReadFile(pth); string(b) != "RIFF-mix" {
		t.Fatalf("preview: %q", b)
	}

	// --- plugin: streamed from A to B, installed and added to FL's database ---
	os.Setenv("HOME", homeA)
	ScanInstalled(cfgA, true)
	rs, err := cfgB.Hub().RequestPlugins("tetramino", []ReqItem{{Plugin: "Serum", Format: "VST3", Vendor: "Xfer Records"}})
	must(t, err)
	if err := ApproveRequest(cfgB, &Job{}, rs[0].ID, ""); err == nil {
		t.Fatal("requester approved own request")
	}
	must(t, ApproveRequest(cfgA, jA, rs[0].ID, ""))
	inA, _ := cfgA.Hub().Inbox()
	if len(inA.ToSend) != 1 {
		t.Fatalf("A to send: %+v", inA.ToSend)
	}
	// the sender packs while HOME is A's, then the receiver installs with B's HOME
	tmpZip := filepath.Join(root, "serum.zip")
	must(t, zipPath(bundle, tmpZip, collectFLDBEntries(bundle)))
	var wg sync.WaitGroup
	var sendErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		sendErr = cfgA.Hub().StreamSend(&Job{}, rs[0].ID, tmpZip)
	}()
	os.Setenv("HOME", homeB)
	ScanInstalled(cfgB, true)
	var ready *PluginRequest
	for i := 0; i < 50 && ready == nil; i++ {
		inB, _ := cfgB.Hub().Inbox()
		for k := range inB.Ready {
			if inB.Ready[k].Status == ReqStreaming {
				ready = &inB.Ready[k]
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ready == nil || ready.Kind != "plugin" {
		t.Fatalf("B doesn't see the transfer: %+v", ready)
	}
	dst, err := ReceiveRequest(cfgB, jB, ready)
	wg.Wait()
	if err != nil || sendErr != nil {
		t.Fatal(err, sendErr, dump(jB))
	}
	if dst != filepath.Join(homeB, ".vst3", "Serum.vst3") {
		t.Fatalf("installed to %s", dst)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "Contents", "Resources", "data.bin")); string(b) != "serum-binary" {
		t.Fatal("plugin content wrong")
	}
	if l, err := os.Readlink(filepath.Join(dst, "Contents", "Current")); err != nil || l != "Resources" {
		t.Fatal("symlink lost", l, err)
	}
	dbB := filepath.Join(homeB, "Documents", "Image-Line", "FL Studio", "Presets", "Plugin database")
	nfo, _ := os.ReadFile(filepath.Join(dbB, "Installed", "Generators", "VST3", "Serum.nfo"))
	if !strings.Contains(string(nfo), "ps_file_filename_0="+dst+"\n") || strings.Contains(string(nfo), "ps_file_date_0=1\n") {
		t.Fatalf("FL database entry not rewritten:\n%s", nfo)
	}
	for _, f := range []string{filepath.Join("Installed", "Generators", "VST3", "Serum.fst"), filepath.Join("Generators", "Synth classic", "Serum.fst")} {
		fst, err := flp.ParseFile(filepath.Join(dbB, f))
		if err != nil {
			t.Fatal(f, err)
		}
		if ps := fst.Info().Plugins; len(ps) != 0 && ps[0].Path != dst {
			t.Fatalf("%s points to %s", f, ps[0].Path)
		}
		raw, _ := os.ReadFile(filepath.Join(dbB, f))
		if !bytes.Contains(raw, []byte(dst)) || bytes.Contains(raw, []byte(bundle)) {
			t.Fatalf("%s not rewritten", f)
		}
	}
	if r, _ := cfgB.Hub().Request(rs[0].ID); r.Status != ReqInstalled {
		t.Fatalf("status %s", r.Status)
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "hub-data")); len(ents) > 2 {
		t.Fatalf("server keeps files: %v", ents)
	}

	// --- the owner removes the friend: GitHub access goes too ---
	must(t, A.RemoveParticipant(cfgB.UserID))
	if cs := gh.Collaborators("alogin/nochnoy-drift"); len(cs) != 0 {
		t.Fatalf("still a collaborator: %v", cs)
	}
}

func TestConnectOldProject(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	cfgA := &Config{Server: hubURL}
	cfgB := &Config{Server: hubURL}
	_, err := cfgA.Hub().Register("tetramino", "uzbek", "tokA")
	must(t, err)
	_, err = cfgB.Hub().Register("friend", "uzbek", "tokB")
	must(t, err)

	// a project of the old version: A's repo, B already a collaborator on GitHub
	full, clone, err := GitHubCreateRepo(cfgA, "Старый трек")
	must(t, err)
	must(t, ghAddCollaborator(cfgA, full, "blogin"))
	must(t, ghAcceptInvitation(cfgB, full))
	dir := filepath.Join(root, "A", "Старый трек")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "Track.flp"), synthFLP("/x/kick.wav"), 0o644)
	A := &Project{Dir: dir, Cfg: cfgA, Job: &Job{}}
	must(t, A.Init(clone))
	_, err = A.Send("старт", false)
	must(t, err)

	B := &Project{Dir: dir, Cfg: cfgB, Job: &Job{}}
	if _, err := B.ConnectToServer(""); err == nil {
		t.Fatal("not the owner, but connected")
	}
	res, err := A.ConnectToServer("")
	must(t, err)
	hp := res.(*HubProject)
	if hp.Name != "Старый трек" || len(hp.Members) != 2 {
		t.Fatalf("members not synced: %+v", hp)
	}
	ps, _ := cfgB.Hub().Projects(0)
	if len(ps) != 1 || ps[0].Repo != full {
		t.Fatalf("B doesn't see the project: %+v", ps)
	}
	// a folder that was never on GitHub gets its own repo
	fresh := filepath.Join(root, "A", "Новый")
	os.MkdirAll(fresh, 0o755)
	os.WriteFile(filepath.Join(fresh, "New.flp"), synthFLP("/x/kick.wav"), 0o644)
	N := &Project{Dir: fresh, Cfg: cfgA, Job: &Job{}}
	must(t, N.Init(""))
	if _, err := N.ConnectToServer(""); err != nil {
		t.Fatal(err, dump(N.Job))
	}
	if cfgA.Hub().ProjectForRemote(N.git().Remote()) == nil {
		t.Fatal("fresh project not on the server")
	}
	_ = exec.Command
}

func TestCrossOSTransfer(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := useFakeGitHub(t, root)
	hubURL := startHub(t, root, gh)
	homeA, homeB := filepath.Join(root, "A-home"), filepath.Join(root, "B-home")

	bundle := filepath.Join(root, "A-plugins", "Serum.vst3")
	os.MkdirAll(filepath.Join(bundle, "Contents", "x86_64-linux"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Contents", "x86_64-linux", "Serum.so"), []byte("bin"), 0o755)
	cfgA := &Config{Server: hubURL, ExtraPluginDirs: []string{filepath.Dir(bundle)}}
	cfgB := &Config{Server: hubURL}
	_, err := cfgA.Hub().Register("tetramino", "uzbek", "tokA")
	must(t, err)
	_, err = cfgB.Hub().Register("friend", "uzbek", "tokB")
	must(t, err)
	os.Setenv("HOME", homeA)
	ScanInstalled(cfgA, true)

	rs, err := cfgB.Hub().RequestPlugins("tetramino", []ReqItem{{Plugin: "Serum", Format: "VST3"}})
	must(t, err)
	req, err := cfgA.Hub().Request(rs[0].ID)
	must(t, err)
	req.FromOS = "Windows" // pretend the friend is on Windows
	in := InspectRequest(cfgA, req)
	if !in.Found || in.Compatible || !strings.Contains(in.Reason, "установщик") {
		t.Fatalf("linux-only bundle must not be sendable to Windows: %+v", in)
	}
	os.MkdirAll(filepath.Join(bundle, "Contents", "x86_64-win"), 0o755)
	if in = InspectRequest(cfgA, req); !in.Compatible {
		t.Fatalf("bundle with a Windows binary must be sendable: %+v", in)
	}
	inst := filepath.Join(homeA, "Downloads", "Serum_x64_Setup.exe")
	os.MkdirAll(filepath.Dir(inst), 0o755)
	os.WriteFile(inst, []byte("MZ installer"), 0o644)
	os.WriteFile(filepath.Join(homeA, "Downloads", "Other plugin.exe"), []byte("MZ"), 0o644)
	if in = InspectRequest(cfgA, req); len(in.Installers) != 1 || in.Installers[0].Path != inst {
		t.Fatalf("installers: %+v", in.Installers)
	}
	must(t, ApproveRequest(cfgA, &Job{}, req.ID, inst))

	var wg sync.WaitGroup
	var sendErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		r, _ := cfgA.Hub().Request(req.ID)
		sendErr = SendApproved(cfgA, &Job{}, r)
	}()
	os.Setenv("HOME", homeB)
	var r *PluginRequest
	for i := 0; i < 50; i++ {
		r, _ = cfgB.Hub().Request(req.ID)
		if r.Status == ReqStreaming {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	dst, err := ReceiveRequest(cfgB, &Job{}, r)
	wg.Wait()
	must(t, err)
	must(t, sendErr)
	if b, _ := os.ReadFile(dst); string(b) != "MZ installer" || filepath.Dir(dst) != filepath.Join(homeB, "Downloads") {
		t.Fatalf("installer not delivered: %s", dst)
	}
	r, _ = cfgB.Hub().Request(req.ID)
	if r.Status != ReqInstalled || r.Kind != "installer" {
		t.Fatalf("final: %+v", r)
	}
	if _, ok := outboxGet(req.ID); ok {
		t.Fatal("outbox not cleaned")
	}
}
