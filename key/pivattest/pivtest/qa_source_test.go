package pivtest

import (
	"testing"
)

// The scanner of TestNoMachineClockNoSleepNoDevicePathInTheSources is held to account on sources that try to get past it. A form that the
// scanner must catch is in qaCaught; a form that it does not catch is a finding (see qa_findings_test.go).
var qaCaught = map[string]string{
	"an aliased import": `package x
import t "time"
func f() { _ = t.Now() }`,
	"an aliased import and Sleep": `package x
import clock "time"
func f() { clock.Sleep(1) }`,
	"a function value": `package x
import "time"
var now = time.Now`,
	"a function value in a local": `package x
import "time"
func f() { g := time.Now; _ = g() }`,
	"a method value of the clock": `package x
import "time"
var f = time.Now().Add`,
	"a method value of the clock, converted": `package x
import "time"
var f = time.Now().UnixNano`,
	"a function value of Sleep passed on": `package x
import "time"
func g(f func(time.Duration)) {}
func h() { g(time.Sleep) }`,
	"in a closure": `package x
import "time"
func f() func() { return func() { time.Sleep(1) } }`,
	"in a struct literal": `package x
import "time"
type s struct{ f func() time.Time }
var v = s{f: time.Now}`,
	"in a method": `package x
import "time"
type c struct{}
func (c) f() int64 { return time.Now().Unix() }`,
	"in a package variable of a group": `package x
import "time"
var (
	a = 1
	b = time.Since
)`,
	"After and AfterFunc and NewTicker": `package x
import "time"
func f() { _ = time.After(1); _ = time.AfterFunc(1, nil); _ = time.NewTicker(1) }`,
	"a blank-aliased second import": `package x
import (
	_ "time"
	t "time"
)
func f() { _ = t.Now() }`,
	"time.Time as a type: the card has no use for a point in time": `package x
import "time"
var t time.Time
func f(a, b time.Time) bool { return a.Before(b) }`,
	"time.Time in a signature": `package x
import "time"
func f(t time.Time) {}`,
	"a device path in a raw string":    "package x\nvar p = `/dev/bus/usb/001/002`\n",
	"a device path with an escape":     `package x` + "\n" + `var p = "\x2fdev/bus/usb"` + "\n",
	"a device path in a constant":      `package x` + "\n" + `const p = "/sys/bus/usb/devices"` + "\n",
	"a device path in a longer string": `package x` + "\n" + `var p = "open /dev/bus/usb/001/002 failed"` + "\n",
	"os imported under an alias":       `package x` + "\n" + `import o "os"` + "\n" + `var _ = o.Args` + "\n",
	"os imported as a blank":           `package x` + "\n" + `import _ "os"` + "\n",
	"os/exec":                          `package x` + "\n" + `import "os/exec"` + "\n" + `var _ = exec.Command` + "\n",
	"net/http":                         `package x` + "\n" + `import "net/http"` + "\n" + `var _ = http.Get` + "\n",
	"crypto/rand":                      `package x` + "\n" + `import "crypto/rand"` + "\n" + `var _ = rand.Reader` + "\n",
	"math/rand/v2":                     `package x` + "\n" + `import "math/rand/v2"` + "\n" + `var _ = rand.IntN` + "\n",
	"math/rand":                        `package x` + "\n" + `import "math/rand"` + "\n" + `var _ = rand.Int` + "\n",
	"syscall":                          `package x` + "\n" + `import "syscall"` + "\n" + `var _ = syscall.Getpid` + "\n",
	"unsafe":                           `package x` + "\n" + `import "unsafe"` + "\n" + `var _ = unsafe.Sizeof(0)` + "\n",
	"testing in a source":              `package x` + "\n" + `import "testing"` + "\n" + `var _ testing.TB` + "\n",
	"plugin":                           `package x` + "\n" + `import "plugin"` + "\n" + `var _ = plugin.Open` + "\n",
}

func TestQaTheSourceCheckCatchesTheFormsItShould(t *testing.T) {
	for name, src := range qaCaught {
		problems, err := sourceProblems("x.go", []byte(src))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(problems) == 0 {
			t.Errorf("%s: the check found nothing in\n%s", name, src)
		}
	}
}

// The check must not cry wolf at what the package is allowed to do.
func TestQaTheSourceCheckLeavesTheAllowedAlone(t *testing.T) {
	for name, src := range map[string]string{
		"time.Duration as a type":    "package x\nimport \"time\"\nfunc f() (d time.Duration, w time.Duration) { return }\n",
		"time constants":             "package x\nimport \"time\"\nvar d = 5 * time.Second\n",
		"a string that says dev":     "package x\nvar s = \"development bus\"\n",
		"a path outside the devices": "package x\nvar s = \"/usr/bus/x\"\n",
		"a field called Now":         "package x\ntype c struct{ Now int }\nfunc f(x c) int { return x.Now }\n",
		"a function called Sleep":    "package x\nfunc Sleep() {}\nfunc f() { Sleep() }\n",
		"sync and encoding/binary":   "package x\nimport (\n\t\"sync\"\n\t\"encoding/binary\"\n)\nvar _ sync.Mutex\nvar _ = binary.BigEndian\n",
	} {
		problems, err := sourceProblems("x.go", []byte(src))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(problems) != 0 {
			t.Errorf("%s: %q", name, problems)
		}
	}
	// The scanner reports a source that does not parse.
	if _, err := sourceProblems("x.go", []byte("package x\nfunc (")); err == nil {
		t.Error("a source that does not parse was accepted")
	}
}
