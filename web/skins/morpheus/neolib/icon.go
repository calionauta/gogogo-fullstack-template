package neo

import (
	"io/fs"
	"os"
	"strings"
	"sync"
)

var (
	iconFSMu sync.RWMutex
	iconFS   = os.DirFS("internal/site/static/icons")
	iconBase = "/static/icons"
)

func SetIconFS(f fs.FS) {
	iconFSMu.Lock()
	iconFS = f
	iconFSMu.Unlock()

	iconCacheMu.Lock()
	iconCache = map[string]string{}
	iconCacheMu.Unlock()
}

func SetIconBase(base string) {
	iconFSMu.Lock()
	iconBase = base
	iconFSMu.Unlock()
}

func IconBase() string {
	iconFSMu.RLock()
	defer iconFSMu.RUnlock()
	return iconBase
}

var (
	iconCacheMu sync.RWMutex
	iconCache   = map[string]string{}
)

func loadIconSVG(name string) string {
	iconCacheMu.RLock()
	v, ok := iconCache[name]
	iconCacheMu.RUnlock()
	if ok {
		return v
	}

	iconFSMu.RLock()
	f := iconFS
	iconFSMu.RUnlock()

	var s string
	if b, err := fs.ReadFile(f, name+".svg"); err == nil {
		text := string(b)
		if i := strings.Index(text, "<svg"); i >= 0 {
			s = text[i:]
		}
	}

	if s == "" {
		return ""
	}
	iconCacheMu.Lock()
	iconCache[name] = s
	iconCacheMu.Unlock()
	return s
}
