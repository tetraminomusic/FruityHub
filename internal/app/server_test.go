package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flsync/internal/fakegh"
	"flsync/internal/hub"
)

// startHub runs our server against the fake GitHub.
func startHub(t *testing.T, root string, gh *fakegh.Server) string {
	data := filepath.Join(root, "hub-data")
	os.MkdirAll(data, 0o700)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	b, _ := json.Marshal(map[string]any{"appId": 1, "slug": "prog-uzbeka-test", "org": gh.Org, "pem": string(pemKey)})
	os.WriteFile(filepath.Join(data, "github-app.json"), b, 0o600)
	s, err := hub.New(hub.Options{DataDir: data, PublicURL: "http://hub.test", GitHubAPI: gh.Srv.URL, MaxFile: 50 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if !s.GitHubApp().Ready() {
		t.Fatal("github app not ready")
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func useFakeGitHub(gh *fakegh.Server) {
	ghAPIBase = gh.Srv.URL
	repoSlug = func(remote string) (string, string, bool) {
		remote = strings.TrimSuffix(filepath.ToSlash(remote), ".git")
		parts := strings.Split(remote, "/")
		if len(parts) < 2 {
			return "", "", false
		}
		return parts[len(parts)-2], parts[len(parts)-1], true
	}
}

func TestServerCollab(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := fakegh.New(root)
	t.Cleanup(gh.Close)
	useFakeGitHub(gh)
	hubURL := startHub(t, root, gh)

	// --- registration: one device = one user, nicks are unique ---
	plugDir := filepath.Join(root, "A-plugins")
	bundle := filepath.Join(plugDir, "Serum.vst3", "Contents")
	os.MkdirAll(filepath.Join(bundle, "Resources"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Resources", "data.bin"), []byte("serum-binary"), 0o755)
	os.Symlink("Resources", filepath.Join(bundle, "Current"))
	cfgA := &Config{Server: hubURL, ExtraPluginDirs: []string{plugDir}}
	cfgB := &Config{Server: hubURL}
	_, err := cfgA.Hub().Register("tetramino")
	must(t, err)
	_, err = cfgB.Hub().Register("friend")
	must(t, err)
	if _, err := (&Config{Server: hubURL}).Hub().Register("FRIEND"); err == nil || !strings.Contains(err.Error(), "занят") {
		t.Fatalf("nick must be unique: %v", err)
	}
	if cfgA.DeviceToken == "" || cfgA.UserID == "" || cfgA.UserName != "tetramino" {
		t.Fatalf("registration not saved: %+v", cfgA)
	}
	us, _ := cfgA.Hub().Users()
	if len(us) != 2 {
		t.Fatalf("users: %+v", us)
	}

	// --- A creates a project on the server and sends it ---
	aDir := filepath.Join(root, "A", "Ночной дрифт")
	os.MkdirAll(aDir, 0o755)
	os.WriteFile(filepath.Join(aDir, "Track.flp"), synthFLP(filepath.Join(aDir, "Audio", "vox.wav")), 0o644)
	os.MkdirAll(filepath.Join(aDir, "Audio"), 0o755)
	os.WriteFile(filepath.Join(aDir, "Audio", "vox.wav"), []byte("RIFF-vocal"), 0o644)
	hp, err := cfgA.Hub().CreateProject("Ночной дрифт")
	must(t, err)
	if !strings.HasPrefix(hp.Repo, "uzbek-org/") || !hp.IsOwner {
		t.Fatalf("project: %+v", hp)
	}
	hp2, err := cfgA.Hub().CreateProject("Ночной дрифт")
	must(t, err)
	if hp2.Repo == hp.Repo {
		t.Fatal("same repo name twice")
	}
	jA := &Job{}
	A := &Project{Dir: aDir, Cfg: cfgA, Job: jA}
	must(t, A.Init(hp.CloneURL))
	if tok := repoToken(cfgA, hp.CloneURL); !strings.HasPrefix(tok, "ghs_") {
		t.Fatalf("no repo token for git: %q", tok)
	}
	if scope := gh.Tokens[repoToken(cfgA, hp.CloneURL)]; len(scope) != 1 || scope[0] != hp.Repo {
		t.Fatalf("token must be limited to the project repo: %v", scope)
	}
	_, err = A.commitAll("start")
	must(t, err)
	if _, err := A.Send("Первая отправка", false); err != nil {
		t.Fatal(err, dump(jA))
	}

	// B is not a member: no access yet
	if hb := cfgB.Hub().ProjectForRemote(hp.CloneURL); hb != nil {
		t.Fatal("B sees a project it is not in")
	}
	if repoToken(cfgB, hp.CloneURL) != "" {
		t.Fatal("B got a token without being a member")
	}

	// --- invitation through the server ---
	if _, err := cfgA.Hub().Invite(hp.ID, "nobody"); err == nil {
		t.Fatal("invited an unknown nick")
	}
	_, err = cfgA.Hub().Invite(hp.ID, "Friend")
	must(t, err)
	in, err := cfgB.Hub().Inbox()
	must(t, err)
	if len(in.Invites) != 1 || in.Invites[0].From.Nick != "tetramino" || in.Invites[0].Name != "Ночной дрифт" {
		t.Fatalf("B inbox: %+v", in)
	}
	acc, err := cfgB.Hub().AcceptInvite(in.Invites[0].ID)
	must(t, err)
	jB := &Job{}
	bDir, err := CloneNamed(cfgB, jB, acc.CloneURL, filepath.Join(root, "B"), acc.Name)
	if err != nil {
		t.Fatal(err, errDetails(err), dump(jB))
	}
	if filepath.Base(bDir) != "Ночной дрифт" {
		t.Fatalf("folder name: %s", bDir)
	}
	if b, _ := os.ReadFile(filepath.Join(bDir, "Audio", "vox.wav")); string(b) != "RIFF-vocal" {
		t.Fatalf("audio not delivered: %q", b)
	}
	B := &Project{Dir: bDir, Cfg: cfgB, Job: jB}
	ps, _ := cfgA.Hub().Project(hp.ID)
	cfgA.Hub().forget()
	ps, _ = cfgA.Hub().Project(hp.ID)
	if len(ps.Members) != 2 || len(ps.Invited) != 0 {
		t.Fatalf("members: %+v", ps)
	}

	// --- comments still go through the meta branch ---
	_, err = A.AddComment("todo", "Нужен вокал во втором куплете")
	must(t, err)
	must(t, B.git().Fetch())
	if cs, _ := B.Comments(); len(cs) != 1 {
		t.Fatalf("B comments: %+v", cs)
	}

	// --- preview uses the release storage with the repo token ---
	wav := filepath.Join(root, "mix.wav")
	os.WriteFile(wav, []byte("RIFF-mix"), 0o644)
	_, err = A.UploadPreview(wav)
	must(t, err)
	os.RemoveAll(filepath.Join(ConfigDir(), "cache"))
	pth, err := B.PreviewFile(jB)
	must(t, err)
	if b, _ := os.ReadFile(pth); string(b) != "RIFF-mix" {
		t.Fatalf("B preview: %q", b)
	}

	// --- plugin transfer through the server ---
	os.Setenv("HOME", filepath.Join(root, "A-home"))
	ScanInstalled(cfgA, true)
	must(t, cfgA.Hub().PutInventory(ScanInstalled(cfgA, false)))
	inv, err := cfgB.Hub().Inventory(cfgA.UserID)
	must(t, err)
	if len(inv.Plugins) == 0 || inv.Plugins[0].Name != "Serum" {
		t.Fatalf("inventory: %+v", inv)
	}
	_, err = cfgA.Hub().UpdateMe(nil, ptr("off"))
	must(t, err)
	if _, err := cfgB.Hub().RequestPlugins("tetramino", []ReqItem{{Plugin: "Serum", Format: "VST3"}}); err == nil {
		t.Fatal("sharing is off but the request went through")
	}
	_, err = cfgA.Hub().UpdateMe(nil, ptr("ask"))
	must(t, err)
	rs, err := cfgB.Hub().RequestPlugins("tetramino", []ReqItem{{Plugin: "Serum", Format: "VST3", Vendor: "Xfer Records"}, {Plugin: "Massive", Format: "VST2"}})
	must(t, err)
	if len(rs) != 2 {
		t.Fatalf("batch request: %+v", rs)
	}
	if again, _ := cfgB.Hub().RequestPlugins("tetramino", []ReqItem{{Plugin: "serum", Format: "VST3"}}); len(again) != 0 {
		t.Fatal("duplicate request accepted")
	}
	if err := ApproveRequest(cfgB, &Job{}, rs[0].ID, ""); err == nil {
		t.Fatal("requester approved own request")
	}
	if _, err := InstallRequest(cfgB, &Job{}, rs[0].ID); err == nil {
		t.Fatal("install before approval")
	}
	inA, _ := cfgA.Hub().Inbox()
	if len(inA.Incoming) != 2 {
		t.Fatalf("A incoming: %+v", inA.Incoming)
	}
	if err := ApproveRequest(cfgA, jA, rs[0].ID, ""); err != nil {
		t.Fatal(err, dump(jA))
	}
	must(t, cfgA.Hub().DeclineRequest(rs[1].ID, "его у меня нет"))

	os.Setenv("HOME", filepath.Join(root, "B-home"))
	ScanInstalled(cfgB, true)
	inB, _ := cfgB.Hub().Inbox()
	if len(inB.Ready) != 1 || inB.Ready[0].Kind != "plugin" || inB.Ready[0].ToNick != "tetramino" {
		t.Fatalf("B ready: %+v", inB.Ready)
	}
	dst, err := InstallRequest(cfgB, jB, rs[0].ID)
	if err != nil {
		t.Fatal(err, dump(jB))
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "Contents", "Resources", "data.bin")); string(b) != "serum-binary" {
		t.Fatal("plugin content wrong")
	}
	if l, err := os.Readlink(filepath.Join(dst, "Contents", "Current")); err != nil || l != "Resources" {
		t.Fatal("symlink lost", l, err)
	}
	if FindInstalled(cfgB, "Serum", "VST3") == nil {
		t.Fatal("installed plugin not found by scan")
	}
	all, _ := cfgB.Hub().Requests()
	st := map[string]string{}
	for _, r := range all {
		st[r.Plugin] = r.Status + "|" + r.Note
	}
	if st["Serum"] != "installed|" || st["Massive"] != "declined|его у меня нет" {
		t.Fatalf("statuses: %v", st)
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "hub-data", "files")); len(ents) != 0 {
		t.Fatal("transfer file left on the server")
	}

	// --- the owner removes the friend: access is gone ---
	must(t, cfgA.Hub().RemoveMember(hp.ID, cfgB.UserID))
	cfgB.Hub().forget()
	cfgB.Hub().mu.Lock()
	cfgB.Hub().tokens = map[string]hubToken{}
	cfgB.Hub().mu.Unlock()
	if repoToken(cfgB, hp.CloneURL) != "" {
		t.Fatal("removed member still gets a token")
	}
}

func ptr(s string) *string { return &s }

func TestCrossOSTransfer(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := fakegh.New(root)
	t.Cleanup(gh.Close)
	useFakeGitHub(gh)
	hubURL := startHub(t, root, gh)
	homeA, homeB := filepath.Join(root, "A-home"), filepath.Join(root, "B-home")

	bundle := filepath.Join(root, "A-plugins", "Serum.vst3")
	os.MkdirAll(filepath.Join(bundle, "Contents", "x86_64-linux"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Contents", "x86_64-linux", "Serum.so"), []byte("bin"), 0o755)
	cfgA := &Config{Server: hubURL, ExtraPluginDirs: []string{filepath.Dir(bundle)}}
	cfgB := &Config{Server: hubURL}
	_, err := cfgA.Hub().Register("tetramino")
	must(t, err)
	_, err = cfgB.Hub().Register("friend")
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

	os.Setenv("HOME", homeB)
	dst, err := InstallRequest(cfgB, &Job{}, req.ID)
	must(t, err)
	if b, _ := os.ReadFile(dst); string(b) != "MZ installer" || filepath.Dir(dst) != filepath.Join(homeB, "Downloads") {
		t.Fatalf("installer not delivered: %s", dst)
	}
	r, _ := cfgB.Hub().Request(req.ID)
	if r.Status != ReqInstalled || r.Kind != "installer" {
		t.Fatalf("final: %+v", r)
	}
}

func TestMoveToServer(t *testing.T) {
	root := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	gh := fakegh.New(root)
	t.Cleanup(gh.Close)
	useFakeGitHub(gh)
	hubURL := startHub(t, root, gh)

	// a project of the old version: own remote, a comment on the meta branch
	old := filepath.Join(root, "old.git")
	must(t, exec.Command("git", "init", "-q", "--bare", "-b", "main", old).Run())
	dir := filepath.Join(root, "A", "Старый трек")
	os.MkdirAll(filepath.Join(dir, "Audio"), 0o755)
	os.WriteFile(filepath.Join(dir, "Audio", "bass.wav"), []byte("RIFF-bass"), 0o644)
	os.WriteFile(filepath.Join(dir, "Track.flp"), synthFLP(filepath.Join(dir, "Audio", "bass.wav")), 0o644)
	cfgA := &Config{Server: hubURL}
	_, err := cfgA.Hub().Register("tetramino")
	must(t, err)
	jA := &Job{}
	A := &Project{Dir: dir, Cfg: cfgA, Job: jA}
	must(t, A.Init(old))
	_, err = A.Send("старт", false)
	must(t, err)
	_, err = A.AddComment("note", "старый комментарий")
	must(t, err)

	if _, err := A.MoveToServer(""); err != nil {
		t.Fatal(err, dump(jA))
	}
	hp := cfgA.Hub().ProjectForRemote(A.git().Remote())
	if hp == nil || hp.Name != "Старый трек" {
		t.Fatalf("not on the server: %v", A.git().Remote())
	}
	// a friend joins and gets everything, comments included
	cfgB := &Config{Server: hubURL}
	_, err = cfgB.Hub().Register("friend")
	must(t, err)
	_, err = cfgA.Hub().Invite(hp.ID, "friend")
	must(t, err)
	in, _ := cfgB.Hub().Inbox()
	acc, err := cfgB.Hub().AcceptInvite(in.Invites[0].ID)
	must(t, err)
	jB := &Job{}
	bDir, err := CloneNamed(cfgB, jB, acc.CloneURL, filepath.Join(root, "B"), acc.Name)
	if err != nil {
		t.Fatal(err, dump(jB))
	}
	if b, _ := os.ReadFile(filepath.Join(bDir, "Audio", "bass.wav")); string(b) != "RIFF-bass" {
		t.Fatalf("audio: %q", b)
	}
	B := &Project{Dir: bDir, Cfg: cfgB, Job: jB}
	if cs, _ := B.Comments(); len(cs) != 1 || cs[0].Text != "старый комментарий" {
		t.Fatalf("comments: %+v", cs)
	}
	if h := B.git().Log(10); len(h) == 0 || h[len(h)-1].Subject != "старт" {
		t.Fatalf("history lost: %+v", h)
	}
}
