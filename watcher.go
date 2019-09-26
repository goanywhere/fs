package fs

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
  "github.com/fsnotify/fsnotify"
)

type watcher struct {
	dir   string
	mutex sync.Mutex

	daemon *fsnotify.Watcher
	event  chan *fsnotify.Event

	ignores   []string
	watchList map[*regexp.Regexp]func(string)
}

func NewWatcher(dir string) *watcher {
	self := new(watcher)
	self.dir = dir
	self.event = make(chan *fsnotify.Event)
	self.ignores = []string{}
	self.watchList = make(map[*regexp.Regexp]func(string))
	return self
}

// IsWrite checks if the triggered event is fsnotify.Write|fsnotify.Create.
func (self *watcher) isWrite(event *fsnotify.Event) bool {
	// instead of MODIFY event, editors may only send CREATE.
	// so we need to capture write & create.
	if event.Op&fsnotify.Write == fsnotify.Write ||
		event.Op&fsnotify.Create == fsnotify.Create {
		return true
	}
	return false
}

// IsRemove checks if the triggered event is fsnotify.Remove.
func (self *watcher) isRemove(event *fsnotify.Event) bool {
	return event.Op&fsnotify.Remove == fsnotify.Remove
}

// Add appends regular expression based pattern processor into the watch list.
func (self *watcher) Add(pattern *regexp.Regexp, process func(path string)) {
	self.mutex.Lock()
	defer self.mutex.Unlock()
	self.watchList[pattern] = process
}

func (self *watcher) watch() {
	self.daemon, _ = fsnotify.NewWatcher()
	if self.daemon != nil {
		self.daemon.Close()
	}
	// ensure we have a new daemon watcher eachtime we start watching.
	self.daemon, _ = fsnotify.NewWatcher()
	if err := self.daemon.Add(self.dir); err != nil {
		log.Fatalf("Failed to create fs watcher for <%s>: %v", self.dir, err)
	}

	// watch all folders under the root.
	filepath.Walk(self.dir, func(path string, info os.FileInfo, e error) error {
		if info.IsDir() {
			for _, ignore := range self.ignores {
				if info.Name() == ignore {
					return filepath.SkipDir
				}
			}
			if err := self.daemon.Add(path); err != nil {
				log.Fatalf("Failed create watch list for (%s): %v", info.Name(), err)
			}
		}
		return e
	})
}

func (self *watcher) startWatching() {
	self.watch()

	var evt *fsnotify.Event
	// multiple events can be triggered on a successful write
	// (e.g. Create followed by multiple CHMOD), just postpone
	// a bit to let it calm before actual processing.
	var delay <-chan time.Time
	for {
		select {
		case event := <-self.daemon.Events:
			// We only need "Write" event (modify | create | remove)
			if self.isWrite(&event) || self.isRemove(&event) {
				evt = &event
				delay = time.After(500 * time.Millisecond)
			}
		case err := <-self.daemon.Errors:
			log.Fatalf("Failed to watch the path %v", err)

		case <-delay:
			self.event <- evt
		}
	}
}

// Start watches all file changes under the root path & dispatch
// to corresonding handlers (added via Add function)
func (self *watcher) Start() {
	go self.startWatching()
	// listens the catched event & start processing.
	for event := range self.event {
		if event == nil {
			continue
		}
		// start processing the event
		var filename = filepath.Base(event.Name)
		for pattern, process := range self.watchList {
			if pattern.MatchString(filename) {
				process(event.Name)
			}
		}
	}
}
