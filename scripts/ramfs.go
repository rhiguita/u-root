package main

import (
	"bytes"
	"debug/elf"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"text/template"
)

type copyfiles struct {
	dir  string
	spec string
}

const (
	// huge suckage here. the 'old' usage is going away but it not gone yet. Just suck in old6a for now.
	// I don't want to revive the 'letter' stuff.
	goList = `{{.Gosrcroot}}
{{.Go}}
go/pkg/include
go/src
go/VERSION.cache
go/misc
go/pkg/tool/{{.Goos}}_{{.Arch}}/compile
go/pkg/tool/{{.Goos}}_{{.Arch}}/link
go/pkg/tool/{{.Goos}}_{{.Arch}}/asm
go/pkg/tool/{{.Goos}}_{{.Arch}}/old6a`
	urootList = `{{.Gopath}}
src`
)

var (
	config struct {
		Goroot     string
		Gosrcroot  string
		Arch       string
		Goos       string
		Gopath     string
		TempDir    string
		Go         string
		Debug      bool
		Fail       bool
		TestChroot bool
		RemoveDir  bool
	}
	letter = map[string]string{
		"amd64": "6",
		"arm":   "5",
		"ppc":   "9",
	}
)

func getenv(e, d string) string {
	v := os.Getenv(e)
	if v == "" {
		v = d
	}
	return v
}

func lsr(n string, w *os.File) error {
	n = n + "/"
	err := filepath.Walk(n, func(name string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		cn := strings.TrimPrefix(name, n)
		fmt.Fprintf(w, "%v\n", cn)
		return nil
	})
	return err
}

// we'll keep using cpio and hope the kernel gets fixed some day.
func cpiop(c string) error {

	t := template.Must(template.New("filelist").Parse(c))
	var b bytes.Buffer
	if err := t.Execute(&b, config); err != nil {
		log.Fatalf("spec %v: %v\n", c, err)
	}

	n := strings.Split(b.String(), "\n")
	if config.Debug {
		log.Printf("Strings :%v:\n", n)
	}

	r, w, err := os.Pipe()
	if err != nil {
		log.Fatalf("%v\n", err)
	}
	cmd := exec.Command("cpio", "--make-directories", "-p", config.TempDir)
	cmd.Dir = n[0]
	cmd.Stdin = r
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if config.Debug {
		log.Printf("Run %v @ %v", cmd, cmd.Dir)
	}
	err = cmd.Start()
	if err != nil {
		log.Printf("%v\n", err)
	}

	for _, v := range n[1:] {
		if config.Debug {
			log.Printf("%v\n", v)
		}
		err := filepath.Walk(path.Join(n[0], v), func(name string, fi os.FileInfo, err error) error {
			if err != nil {
				log.Printf(" WALK FAIL%v: %v\n", name, err)
				// That's ok, sometimes things are not there.
				return filepath.SkipDir
			}
			cn := strings.TrimPrefix(name, n[0]+"/")
			if cn == ".git" {
				return filepath.SkipDir
			}
			fmt.Fprintf(w, "%v\n", cn)
			//log.Printf("c.dir %v %v %v\n", n[0], name, cn)
			return nil
		})
		if err != nil {
			log.Printf("%s: %v\n", v, err)
		}
	}
	w.Close()
	err = cmd.Wait()
	if err != nil {
		log.Printf("%v\n", err)
	}
	return nil
}

func sanity() {
	goBinGo := path.Join(config.Gosrcroot, "go/bin/go")
	log.Printf("check %v as the go binary", goBinGo)
	_, err := os.Stat(goBinGo)
	if err == nil {
		config.Go = "go/bin/go"
	}
	// but does the one in go/bin/OS_ARCH exist too?
	archgo := fmt.Sprintf("bin/%s_%s/go", config.Goos, config.Arch)
	linuxBinGo := path.Join(config.Gosrcroot, archgo)
	log.Printf("check %v as the go binary", linuxBinGo)
	_, err = os.Stat(linuxBinGo)
	if err == nil {
		config.Go = archgo
		goBinGo = linuxBinGo
	}
	log.Printf("Using %v as the go command", goBinGo)
	if config.Go == "" {
		log.Fatalf("Can't find a go binary! Is GOROOT set correctly?")
	}
	f, err := elf.Open(goBinGo)
	if err != nil {
		log.Fatalf("%v is not an ELF file; don't know what to do", goBinGo)
	}
	ds := f.SectionByType(elf.SHT_DYNAMIC)
	if ds != nil {
		log.Printf("U-root requires a staticically built go tree at present. %v is dynamic.", goBinGo)
		log.Fatalf("To fix this:\ncd %v/src\nexport CGO_ENABLED=0\nGOARCH=%v ./make.bash", config.Goroot,config.Arch)
	}
}

// It's annoying asking them to set lots of things. So let's try to figure it out.
func guessgoroot() {
	config.Goroot = os.Getenv("GOROOT")
	if config.Goroot != "" {
		log.Printf("Using %v from the environment as the GOROOT", config.Goroot)
		config.Gosrcroot = path.Dir(config.Goroot)
		return
	}
	log.Print("Goroot is not set, trying to find a go binary")
	p := os.Getenv("PATH")
	paths := strings.Split(p, ":")
	for _, v := range paths {
		g := path.Join(v, "go")
		if _, err := os.Stat(g); err == nil {
			config.Goroot = path.Dir(path.Dir(v))
			config.Gosrcroot = path.Dir(config.Goroot)
			log.Printf("Guessing that goroot is %v from $PATH", config.Goroot)
			return
		}
	}
	log.Printf("GOROOT is not set and can't find a go binary in %v", p)
	config.Fail = true
}

func guessgopath() {
	defer func() {
		config.Gosrcroot = path.Dir(config.Goroot)
	}()
	gopath := os.Getenv("GOPATH")
	if gopath != "" {
		config.Gopath = path.Clean(gopath)
		return
	}
	// It's a good chance they're running this from the u-root source directory
	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("GOPATH was not set and I can't get the wd: %v", err)
		config.Fail = true
		return
	}
	// walk up the cwd until we find a u-root entry. See if src/cmds/init/init.go exists.
	for c := cwd; c != "/"; c = path.Dir(c) {
		if path.Base(c) != "u-root" {
			continue
		}
		check := path.Join(c, "src/cmds/init/init.go")
		if _, err := os.Stat(check); err != nil {
			//log.Printf("Could not stat %v", check)
			continue
		}
		config.Gopath = c
		log.Printf("Guessing %v as GOPATH", c)
		os.Setenv("GOPATH", c)
		return
	}
	config.Fail = true
	log.Printf("GOPATH was not set, and I can't see a u-root-like name in %v", cwd)
	return
}

// sad news. If I concat the Go cpio with the other cpios, for reasons I don't understand,
// the kernel can't unpack it. Don't know why, don't care. Need to create one giant cpio and unpack that.
// It's not size related: if the go archive is first or in the middle it still fails.
func main() {
	flag.BoolVar(&config.Debug, "d", false, "Debugging")
	flag.BoolVar(&config.TestChroot, "test", false, "test the directory by chrooting to it")
	flag.BoolVar(&config.RemoveDir, "removedir", true, "remove the directory when done -- cleared if test fails")
	flag.Parse()
	var err error
	config.Arch = getenv("GOARCH", "amd64")
	config.Go = ""
	config.Goos = "linux"
	guessgoroot()
	guessgopath()
	if config.Fail {
		log.Fatal("Setup failed")
	}
	config.TempDir, err = ioutil.TempDir("", "u-root")
	if err != nil {
		log.Fatalf("%v", err)
	}
	defer func() {
		if config.RemoveDir {
			log.Printf("Removing %v\n", config.TempDir)
			if err := os.RemoveAll(config.TempDir); err != nil {
				log.Printf("Can't remove %v: %v", config.TempDir, err)
			}
		}
	}()

	// sanity checking: do /go/bin/go, and some basic source files exist?
	sanity()
	// Build init
	cmd := exec.Command("go", "build", "init.go")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Dir = path.Join(config.Gopath, "src/cmds/init")

	err = cmd.Run()
	if err != nil {
		log.Fatalf("%v\n", err)
		os.Exit(1)
	}

	// These produce arrays of strings, the first element being the
	// directory to walk from.
	cpio := []string{
		goList,
		urootList,
	}
	for _, c := range cpio {
		if err := cpiop(c); err != nil {
			log.Printf("Things went south. TempDir is %v", config.TempDir)
			log.Fatalf("Bailing out near line 666")
		}
	}

	// Drop an init in /
	initbin, err := ioutil.ReadFile(path.Join(config.Gopath, "src/cmds/init/init"))
	if err != nil {
		log.Fatal("%v\n", err)
	}
	err = ioutil.WriteFile(path.Join(config.TempDir, "init"), initbin, 0755)
	if err != nil {
		log.Fatal("%v\n", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		log.Fatalf("%v\n", err)
	}

	// First create the archive and put the device cpio in it.
	// Note that Gopath is also the base of all of u-root.
	dev, err := ioutil.ReadFile(path.Join(config.Gopath, "scripts/dev.cpio"))
	if err != nil {
		log.Fatal("%v %v\n", dev, err)
	}

	oname := fmt.Sprintf("/tmp/initramfs.%v_%v.cpio", config.Goos, config.Arch)
	if err := ioutil.WriteFile(oname, dev, 0600); err != nil {
		log.Fatal("%v\n", err)
	}

	// Now use the append option for cpio to append to it.
	// That way we get one cpio.
	cmd = exec.Command("cpio", "-H", "newc", "-o", "-A", "-F", oname)
	cmd.Dir = config.TempDir
	cmd.Stdin = r
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if config.Debug {
		log.Printf("Run %v @ %v", cmd, cmd.Dir)
	}
	err = cmd.Start()
	if err != nil {
		log.Fatalf("%v\n", err)
	}
	if err := lsr(config.TempDir, w); err != nil {
		log.Fatal("%v\n", err)
	}
	w.Close()
	err = cmd.Wait()
	if err != nil {
		log.Printf("%v\n", err)
	}
	defer func() {
		log.Printf("Output file is in %v\n", oname)
	}()

	if !config.TestChroot {
		return
	}

	// We need to populate the temp directory with dev.cpio. It's a chicken and egg thing;
	// we can't run init without, e.g., /dev/console and /dev/null.
	cmd = exec.Command("sudo", "cpio", "-i")
	cmd.Dir = config.TempDir
	// We have it in memory. Get a better way to do this!
	r, err = os.Open(path.Join(config.Gopath, "scripts/dev.cpio"))
	if err != nil {
		log.Fatalf("%v", err)
	}
	// OK, at this point, we know we can run as root. And, we're going to create things
	// we can only remove as root. So, we'll have to remove the directory with
	// extreme measures.
	config.RemoveDir = false
	cmd.Stdin, cmd.Stderr, cmd.Stdout = r, os.Stderr, os.Stdout
	if config.Debug {
		log.Printf("Run %v @ %v", cmd, cmd.Dir)
	}
	err = cmd.Run()
	if err != nil {
		log.Fatalf("%v", err)
	}
	// Arrange to start init in the directory in a new namespace.
	// That should make all mounts go away when we're done.
	// On real kernels you can unshare without being root. Not on Linux.
	cmd = exec.Command("sudo", "unshare", "-m", "chroot", config.TempDir, "/init")
	cmd.Dir = config.TempDir
	cmd.Stdin, cmd.Stderr, cmd.Stdout = os.Stdin, os.Stderr, os.Stdout
	if config.Debug {
		log.Printf("Run %v @ %v", cmd, cmd.Dir)
	}
	err = cmd.Run()
	if err != nil {
		log.Fatalf("Test failed, not removing %v: %v", config.TempDir, err)
	}
	// Wow, this one is *scary*
	cmd = exec.Command("sudo", "rm", "-rf", config.TempDir)
	cmd.Stderr, cmd.Stdout = os.Stderr, os.Stdout
	err = cmd.Run()
	if err != nil {
		log.Fatalf("%v", err)
	}
}
