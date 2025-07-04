package main

import (
	"flag"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Watcher struct {
	dir          string
	interval     time.Duration
	buildCmd     string
	runCmd       string
	includes     []string
	excludes     []string
	depFile      string
	depCmd       string
	prevHash     uint64
	prevDepMTime time.Time
	process      *exec.Cmd
	processMu    sync.Mutex
}

func NewWatcher(dir string, interval time.Duration, buildCmd, runCmd, depFile, depCmd string, includes, excludes []string) *Watcher {
	return &Watcher{
		dir:      dir,
		interval: interval,
		buildCmd: buildCmd,
		runCmd:   runCmd,
		depFile:  depFile,
		depCmd:   depCmd,
		includes: includes,
		excludes: excludes,
	}
}

func (w *Watcher) shouldProcess(relPath string) bool {
	for _, ex := range w.excludes {
		if strings.HasPrefix(relPath, ex) || strings.HasSuffix(relPath, ex) {
			return false
		}
	}
	if len(w.includes) == 0 {
		return true
	}
	for _, in := range w.includes {
		if strings.HasPrefix(relPath, in) || strings.HasSuffix(relPath, in) {
			return true
		}
	}
	return false
}

func (w *Watcher) hashDir() (uint64, bool, error) {
	h := fnv.New64a()
	depChanged := false

	err := filepath.Walk(w.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error accessing %s: %v", path, err)
			return nil
		}
		if info == nil {
			log.Printf("No info for %s", path)
			return nil
		}

		relPath, _ := filepath.Rel(w.dir, path)

		if info.IsDir() {
			// Skip hidden subdirs, but not root
			if info.Name() != "." && info.Name()[0] == '.' {
				return filepath.SkipDir
			}
			return nil
		}

		// Apply file excludes
		if !w.shouldProcess(relPath) {
			return nil
		}

		// Include in hash
		h.Write([]byte(relPath))
		h.Write([]byte(fmt.Sprintf("%d", info.Size())))
		h.Write([]byte(info.ModTime().String()))

		// Check dep file change
		if w.depFile != "" && filepath.Base(path) == filepath.Base(w.depFile) {
			if info.ModTime() != w.prevDepMTime {
				depChanged = true
				w.prevDepMTime = info.ModTime()
			}
		}
		return nil
	})

	if err != nil {
		return 0, false, err
	}
	return h.Sum64(), depChanged, nil
}

func (w *Watcher) runShell(command string) error {
	if command == "" {
		return nil
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (w *Watcher) runBuild(depChanged bool) error {
	if depChanged && w.depCmd != "" {
		log.Printf("%s changed: running %s...\n", w.depFile, w.depCmd)
		if err := w.runShell(w.depCmd); err != nil {
			return err
		}
	}
	log.Println("Running build command...")
	return w.runShell(w.buildCmd)
}

func (w *Watcher) startApp() error {
	w.processMu.Lock()
	defer w.processMu.Unlock()

	if w.process != nil && w.process.Process != nil {
		log.Println("Stopping previous app process...")
		_ = w.process.Process.Kill()
		w.process = nil
	}

	log.Println("Starting app...")
	cmd := exec.Command("/bin/sh", "-c", w.runCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	w.process = cmd
	go func() {
		_ = cmd.Wait()
		log.Println("App exited")
		w.processMu.Lock()
		w.process = nil
		w.processMu.Unlock()
	}()
	return nil
}

func (w *Watcher) Run() {
	for {
		hash, depChanged, err := w.hashDir()
		if err != nil {
			log.Println("Error hashing dir:", err)
			time.Sleep(w.interval)
			continue
		}

		if hash != w.prevHash {
			log.Println("Change detected, rebuilding...")
			w.prevHash = hash

			if err := w.runBuild(depChanged); err != nil {
				log.Println("Build failed:", err)
				time.Sleep(w.interval)
				continue
			}

			if err := w.startApp(); err != nil {
				log.Println("App start failed:", err)
			}
		}

		time.Sleep(w.interval)
	}
}

func printBanner() {
	fmt.Println("🚀 poly-watcher — The universal build-run watcher for your projects. Change it. Build it. Run it. Repeat.")
	fmt.Println("Example:")
	fmt.Println(`  poly-watcher --root=./myapp --depfile=go.mod --depcommand="go mod tidy && go mod download" --build="go build -o myapp ." --run="./myapp" --include=.go --exclude=.git,.polycode`)
	fmt.Println()
}

func main() {
	printBanner()

	buildCmd := flag.String("build", "echo 'No build command specified'", "Build command to run on change")
	runCmd := flag.String("run", "echo 'No run command specified'", "Run command to execute built app")
	depFile := flag.String("depfile", "", "Dependency file to monitor for changes (e.g. go.mod, package.json)")
	depCmd := flag.String("depcommand", "", "Command to run when dependency file changes (e.g. 'go mod tidy', 'npm install')")
	interval := flag.Duration("interval", 1*time.Second, "Polling interval (e.g. 1s, 500ms)")
	includeDirs := flag.String("include", "", "Comma-separated list of include rules (prefix or suffix, e.g. '.go,services')")
	excludeDirs := flag.String("exclude", "", "Comma-separated list of exclude rules (prefix or suffix, e.g. '.git,tmp')")

	flag.Parse()

	includes := []string{}
	excludes := []string{}
	if *includeDirs != "" {
		includes = strings.Split(*includeDirs, ",")
	}
	if *excludeDirs != "" {
		excludes = strings.Split(*excludeDirs, ",")
	}

	watcher := NewWatcher(".", *interval, *buildCmd, *runCmd, *depFile, *depCmd, includes, excludes)
	log.Println("Starting poly-watcher...")
	watcher.Run()
}
