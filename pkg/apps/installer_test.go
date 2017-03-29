package apps

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cozy/checkup"
	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/vfs"
	"github.com/cozy/cozy-stack/pkg/vfs/vfsafero"
	"github.com/stretchr/testify/assert"
)

var localGitCmd *exec.Cmd
var localGitDir string
var localVersion = "1.0.0"
var ts *httptest.Server

type transport struct{}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := new(http.Request)
	*req2 = *req
	req2.URL, _ = url.Parse(ts.URL)
	return http.DefaultTransport.RoundTrip(req2)
}

func fileContainsBytes(fs vfs.VFS, name string, data []byte) (bool, error) {
	doc, err := fs.FileByPath(name)
	if err != nil {
		return false, err
	}
	f, err := fs.OpenFile(doc)
	if err != nil {
		return false, err
	}
	defer f.Close()
	b, err := ioutil.ReadAll(f)
	if err != nil {
		return false, err
	}
	return bytes.Contains(b, data), nil
}

func manifest() string {
	return strings.Replace(`{
  "description": "A mini app to test cozy-stack-v2",
  "developer": {
    "name": "Bruno",
    "url": "cozy.io"
  },
  "license": "MIT",
  "name": "mini-app",
  "permissions": {},
  "slug": "mini",
  "version": "`+localVersion+`"
}`, "\n", "", -1)
}

func serveGitRep() {
	dir, err := ioutil.TempDir("", "cozy-app")
	if err != nil {
		panic(err)
	}
	localGitDir = dir
	args := `
echo '` + manifest() + `' > manifest.webapp && \
git init . && \
git add . && \
git commit -m 'Initial commit' && \
git checkout -b branch && \
echo 'branch' > branch && \
git add . && \
git commit -m 'Create a branch' && \
git checkout -`
	cmd := exec.Command("sh", "-c", args)
	cmd.Dir = localGitDir
	if err := cmd.Run(); err != nil {
		panic(err)
	}

	// "git daemon --reuseaddr --base-path=./ --export-all ./.git"
	localGitCmd = exec.Command("git", "daemon", "--reuseaddr", "--base-path=./", "--export-all", "./.git")
	localGitCmd.Dir = localGitDir
	if out, err := localGitCmd.CombinedOutput(); err != nil {
		fmt.Println(string(out))
		panic(err)
	}
}

func doUpgrade(major int) {
	localVersion = fmt.Sprintf("%d.0.0", major)
	args := `
echo '` + manifest() + `' > manifest.webapp && \
git commit -am "Upgrade commit" && \
git checkout - && \
echo '` + manifest() + `' > manifest.webapp && \
git commit -am "Upgrade commit" && \
git checkout -`
	cmd := exec.Command("sh", "-c", args)
	cmd.Dir = localGitDir
	if err := cmd.Run(); err != nil {
		panic(err)
	}
}

var db couchdb.Database
var fs vfs.VFS

func TestInstallBadSlug(t *testing.T) {
	_, err := NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		SourceURL: "git://foo.bar",
	})
	if assert.Error(t, err) {
		assert.Equal(t, ErrInvalidSlugName, err)
	}

	_, err = NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "coucou/",
		SourceURL: "git://foo.bar",
	})
	if assert.Error(t, err) {
		assert.Equal(t, ErrInvalidSlugName, err)
	}
}

func TestInstallBadAppsSource(t *testing.T) {
	_, err := NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "app3",
		SourceURL: "foo://bar.baz",
	})
	if assert.Error(t, err) {
		assert.Equal(t, ErrNotSupportedSource, err)
	}

	_, err = NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "app4",
		SourceURL: "git://bar  .baz",
	})
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "invalid character")
	}
}

func TestInstallSuccessful(t *testing.T) {
	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "local-cozy-mini",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Install()

	var state State
	for {
		man, done, err2 := inst.Poll()
		if !assert.NoError(t, err2) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Installing, man.State()) {
				return
			}
		} else if state == Installing {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}

	ok, err := vfs.Exists(fs, "/.cozy_apps/local-cozy-mini/manifest.webapp")
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = fileContainsBytes(fs, "/.cozy_apps/local-cozy-mini/manifest.webapp", []byte("1.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
}

func TestInstallAldreadyExist(t *testing.T) {
	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "cozy-app-a",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Install()

	for {
		var done bool
		_, done, err = inst.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if done {
			break
		}
	}

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "cozy-app-a",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Install()

	_, _, err = inst.Poll()
	assert.Equal(t, ErrAlreadyExists, err)
}

func TestInstallWithUpgrade(t *testing.T) {
	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "cozy-app-b",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Install()

	for {
		var done bool
		_, done, err = inst.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if done {
			break
		}
	}

	ok, err := vfs.Exists(fs, "/.cozy_apps/local-cozy-mini/manifest.webapp")
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = fileContainsBytes(fs, "/.cozy_apps/local-cozy-mini/manifest.webapp", []byte("1.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")

	doUpgrade(2)

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "cozy-app-b",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Update()

	var state State
	for {
		man, done, err2 := inst.Poll()
		if !assert.NoError(t, err2) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Upgrading, man.State()) {
				return
			}
		} else if state == Upgrading {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}

	ok, err = vfs.Exists(fs, "/.cozy_apps/cozy-app-b/manifest.webapp")
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = fileContainsBytes(fs, "/.cozy_apps/cozy-app-b/manifest.webapp", []byte("2.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
}

func TestInstallAndUpgradeWithBranch(t *testing.T) {
	doUpgrade(3)

	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "local-cozy-mini-branch",
		SourceURL: "git://localhost/#branch",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Install()

	var state State
	for {
		man, done, err2 := inst.Poll()
		if !assert.NoError(t, err2) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Installing, man.State()) {
				return
			}
		} else if state == Installing {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}

	ok, err := vfs.Exists(fs, "/.cozy_apps/local-cozy-mini-branch/manifest.webapp")
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = fileContainsBytes(fs, "/.cozy_apps/local-cozy-mini-branch/manifest.webapp", []byte("3.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
	ok, err = vfs.Exists(fs, "/.cozy_apps/local-cozy-mini-branch/branch")
	assert.NoError(t, err)
	assert.True(t, ok, "The good branch was checked out")

	doUpgrade(4)

	inst, err = NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "local-cozy-mini-branch",
		SourceURL: "git://localhost/#branch",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Update()

	state = ""
	for {
		man, done, err2 := inst.Poll()
		if !assert.NoError(t, err2) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Upgrading, man.State()) {
				return
			}
		} else if state == Upgrading {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}

	ok, err = vfs.Exists(fs, "/.cozy_apps/local-cozy-mini-branch/manifest.webapp")
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest is present")
	ok, err = fileContainsBytes(fs, "/.cozy_apps/local-cozy-mini-branch/manifest.webapp", []byte("4.0.0"))
	assert.NoError(t, err)
	assert.True(t, ok, "The manifest has the right version")
	ok, err = vfs.Exists(fs, "/.cozy_apps/local-cozy-mini-branch/branch")
	assert.NoError(t, err)
	assert.True(t, ok, "The good branch was checked out")
}

func TestInstallFromGithub(t *testing.T) {
	inst, err := NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "github-cozy-mini",
		SourceURL: "git://github.com/nono/cozy-mini.git",
	})
	if !assert.NoError(t, err) {
		return
	}

	go inst.Install()

	var state State
	for {
		man, done, err := inst.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if state == "" {
			if !assert.EqualValues(t, Installing, man.State()) {
				return
			}
		} else if state == Installing {
			if !assert.EqualValues(t, Ready, man.State()) {
				return
			}
			if !assert.True(t, done) {
				return
			}
			break
		} else {
			t.Fatalf("invalid state")
			return
		}
		state = man.State()
	}
}

func TestUninstall(t *testing.T) {
	inst1, err := NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "github-cozy-delete",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}
	go inst1.Install()
	for {
		var done bool
		_, done, err = inst1.Poll()
		if !assert.NoError(t, err) {
			return
		}
		if done {
			break
		}
	}
	inst2, err := NewInstaller(db, fs, &InstallerOptions{
		Type: Webapp,
		Slug: "github-cozy-delete",
	})
	if !assert.NoError(t, err) {
		return
	}
	_, err = inst2.Delete()
	if !assert.NoError(t, err) {
		return
	}
	inst3, err := NewInstaller(db, fs, &InstallerOptions{
		Type:      Webapp,
		Slug:      "github-cozy-delete",
		SourceURL: "git://localhost/",
	})
	if !assert.NoError(t, err) {
		return
	}
	go inst3.Update()
	_, _, err = inst3.Poll()
	assert.Error(t, err)
}

func TestMain(m *testing.M) {
	config.UseTestFile()

	check, err := checkup.HTTPChecker{URL: config.CouchURL()}.Check()
	if err != nil || check.Status() != checkup.Healthy {
		fmt.Println("This test need couchdb to run.")
		os.Exit(1)
	}

	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, manifest())
	}))

	manifestClient = &http.Client{
		Transport: &transport{},
	}

	db = couchdb.SimpleDatabasePrefix("apps-test")
	index := vfs.NewCouchdbIndexer(db)

	err = couchdb.ResetDB(db, consts.Apps)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = couchdb.ResetDB(db, consts.Files)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	fs, err = vfsafero.New(index, &url.URL{Scheme: "mem"}, "cozy.local")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	go serveGitRep()

	time.Sleep(100 * time.Millisecond)

	err = couchdb.ResetDB(db, consts.Permissions)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = couchdb.DefineIndexes(db, consts.IndexesByDoctype(consts.Files))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = couchdb.DefineIndexes(db, consts.IndexesByDoctype(consts.Permissions))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = fs.InitFs()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	res := m.Run()

	couchdb.DeleteDB(db, consts.Apps)
	couchdb.DeleteDB(db, consts.Files)
	ts.Close()

	localGitCmd.Process.Signal(os.Interrupt)

	os.Exit(res)
}
