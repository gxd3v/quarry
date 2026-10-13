package term

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const helperEnv = "QUARRY_TERM_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// helper is the child process the tests run in the terminal.
func helper(mode string) {
	switch mode {
	case "hello":
		fmt.Println("hello-from-helper")
	case "exit":
		fmt.Println("exiting")
		os.Exit(7)
	case "echo":
		fmt.Println("ready")
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			switch line := strings.TrimSpace(sc.Text()); line {
			case "quit":
				os.Exit(3)
			case "size":
				cols, rows := termSize()
				fmt.Printf("size:%dx%d\n", cols, rows)
			default:
				fmt.Printf("got:%s\n", line)
			}
		}
	case "tree":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), helperEnv+"=sleep")
		if err := child.Start(); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Printf("child:%d\n", child.Process.Pid)
		time.Sleep(time.Hour)
	case "sleep":
		time.Sleep(time.Hour)
	}
}

func startHelper(t *testing.T, mode string, cols, rows int) (*Term, *output) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	tm, err := Start(cmd, cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tm.Close() })
	return tm, collect(tm)
}

// output accumulates what the terminal prints.
type output struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
}

func collect(r io.Reader) *output {
	o := &output{done: make(chan struct{})}
	go func() {
		defer close(o.done)
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			o.mu.Lock()
			o.buf.Write(b[:n])
			o.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return o
}

// text is the output without escape sequences and carriage returns.
func (o *output) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.ReplaceAll(ansi.Strip(o.buf.String()), "\r", "")
}

func (o *output) waitFor(t *testing.T, re string) []string {
	t.Helper()
	rx := regexp.MustCompile(re)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := rx.FindStringSubmatch(o.text()); m != nil {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output never matched %q; got:\n%q", re, o.text())
	return nil
}

func (o *output) waitEOF(t *testing.T) {
	t.Helper()
	select {
	case <-o.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("output never ended; got:\n%q", o.text())
	}
}

func waitExit(t *testing.T, tm *Term) (int, error) {
	t.Helper()
	select {
	case <-tm.Done():
		return tm.Wait()
	case <-time.After(10 * time.Second):
		t.Fatal("process did not exit")
		return 0, nil
	}
}

func TestOutput(t *testing.T) {
	tm, out := startHelper(t, "hello", 0, 0)
	out.waitFor(t, "hello-from-helper")
	out.waitEOF(t)
	if code, err := waitExit(t, tm); code != 0 || err != nil {
		t.Fatalf("Wait = %d, %v", code, err)
	}
	if err := tm.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := tm.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestExitCode(t *testing.T) {
	tm, out := startHelper(t, "exit", 0, 0)
	out.waitFor(t, "exiting")
	out.waitEOF(t)
	if code, err := waitExit(t, tm); code != 7 || err != nil {
		t.Fatalf("Wait = %d, %v; want 7", code, err)
	}
}

func TestInputAndResize(t *testing.T) {
	tm, out := startHelper(t, "echo", 100, 30)
	out.waitFor(t, "ready")

	if _, err := io.WriteString(tm, "ping\r"); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "got:ping")
	// Line discipline echo: the typed text appears before the reply.
	if txt := out.text(); strings.Index(txt, "ping") >= strings.Index(txt, "got:ping") {
		t.Errorf("input not echoed: %q", txt)
	}

	io.WriteString(tm, "size\r")
	out.waitFor(t, "size:100x30")
	if err := tm.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	io.WriteString(tm, "size\r")
	out.waitFor(t, "size:120x40")
	if err := tm.Resize(0, 10); err == nil {
		t.Error("Resize(0, 10) succeeded")
	}

	io.WriteString(tm, "quit\r")
	out.waitEOF(t)
	if code, err := waitExit(t, tm); code != 3 || err != nil {
		t.Fatalf("Wait = %d, %v; want 3", code, err)
	}
}

func TestCloseKillsTree(t *testing.T) {
	tm, out := startHelper(t, "tree", 0, 0)
	m := out.waitFor(t, `child:(\d+)`)
	child, _ := strconv.Atoi(m[1])

	closed := make(chan error, 1)
	go func() { closed <- tm.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung")
	}
	select {
	case <-tm.Done():
	default:
		t.Fatal("process still running after Close")
	}
	out.waitEOF(t)
	waitGone(t, child)

	if _, err := tm.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("Write after Close: %v, want ErrClosed", err)
	}
	if err := tm.Resize(80, 24); err == nil {
		t.Error("Resize after Close succeeded")
	}
	if n, err := tm.Read(make([]byte, 10)); n != 0 || err != io.EOF {
		t.Errorf("Read after Close = %d, %v; want EOF", n, err)
	}
}

func TestStartErrors(t *testing.T) {
	if _, err := Start(exec.Command("quarry-no-such-command-x9"), 0, 0); err == nil {
		t.Error("missing command started")
	}
	if _, err := Start(exec.Command(os.Args[0]), MaxSize+1, 10); err == nil {
		t.Error("oversized terminal started")
	}
	cmd := exec.Command(os.Args[0])
	cmd.Stdout = io.Discard
	if _, err := Start(cmd, 0, 0); err == nil {
		t.Error("command with stdout started")
	}
}
