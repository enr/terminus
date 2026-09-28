// Package remote runs terminus on other machines through the system ssh client: it copies the
// binary for the remote architecture (cached by hash), runs a report and brings back the JSON.
//
// Using the ssh binary rather than an ssh library means that ~/.ssh/config (aliases, users,
// ports, ProxyJump, identities), the agent and known_hosts verification work as they do for the
// operator, with nothing to configure in terminus.
package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/runner"
)

// Options tune a remote run.
type Options struct {
	// SSH is the ssh client (default "ssh"); SSHOptions are passed as -o options.
	SSH        string
	SSHOptions []string
	// Sudo runs the remote terminus with sudo -n (non interactive).
	Sudo bool
	// RemoteBinary is a terminus already installed on the hosts: nothing is copied.
	RemoteBinary string
	// BinariesDir holds terminus-linux-<arch> binaries for architectures other than the local one.
	BinariesDir string
	// RemoteConfig is a local terminus.toml copied to the hosts and used there.
	RemoteConfig string
	// Args are appended to the remote "terminus report -o json".
	Args []string
	// Parallel is the number of hosts processed at once.
	Parallel int
	// Timeout bounds each host (connection, copy and run).
	Timeout time.Duration

	// Executable and Arch describe the local binary (tests replace them).
	Executable func() (string, error)
	Arch       string
}

// Result is the outcome for a host.
type Result struct {
	Host   string        `json:"host"`
	Report *model.Report `json:"report,omitempty"`
	Error  string        `json:"error,omitempty"`
	// Arch is the remote architecture; Copied tells whether the binary was uploaded this time.
	Arch   string `json:"arch,omitempty"`
	Copied bool   `json:"copied"`
}

// ExitCode is the exit code of a host: the one of its report, or 3 when it failed.
func (r Result) ExitCode() int {
	if r.Error != "" || r.Report == nil {
		return model.ExitError
	}
	return r.Report.ExitCode()
}

// Run processes the hosts and returns their results in the same order.
func Run(ctx context.Context, rn runner.Runner, hosts []string, o Options) []Result {
	if o.SSH == "" {
		o.SSH = "ssh"
	}
	if o.Parallel <= 0 {
		o.Parallel = 4
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.Executable == nil {
		o.Executable = os.Executable
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	out := make([]Result, len(hosts))
	sem := make(chan struct{}, o.Parallel)
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hctx, cancel := context.WithTimeout(ctx, o.Timeout)
			defer cancel()
			out[i] = runHost(hctx, rn, h, o)
		}()
	}
	wg.Wait()
	return out
}

// target is a host as given on the command line: [user@]host[:port].
type target struct {
	dest string
	port string
}

func parseHost(h string) (target, error) {
	if h == "" || strings.HasPrefix(h, "-") {
		return target{}, fmt.Errorf("invalid host %q", h)
	}
	user, host := "", h
	if i := strings.LastIndex(h, "@"); i >= 0 {
		user, host = h[:i+1], h[i+1:]
	}
	// host:port and [ipv6]:port; a bare IPv6 address has more than one colon.
	if strings.HasPrefix(host, "[") || strings.Count(host, ":") == 1 {
		if hh, port, err := net.SplitHostPort(host); err == nil {
			if _, err := strconv.ParseUint(port, 10, 16); err != nil {
				return target{}, fmt.Errorf("invalid port in %q", h)
			}
			return target{dest: user + hh, port: port}, nil
		}
	}
	return target{dest: user + host}, nil
}

// ssh builds an ssh command running a shell command line on the host.
func (o Options) ssh(t target, command string, stdin io.Reader) runner.Cmd {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}
	for _, opt := range o.SSHOptions {
		args = append(args, "-o", opt)
	}
	if t.port != "" {
		args = append(args, "-p", t.port)
	}
	args = append(args, "--", t.dest, command)
	return runner.Cmd{Name: o.SSH, Args: args, Stdin: stdin, Timeout: runner.NoTimeout}
}

// archs maps uname -m to Go architectures.
var archs = map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}

func runHost(ctx context.Context, rn runner.Runner, host string, o Options) Result {
	res := Result{Host: host}
	fail := func(format string, args ...any) Result {
		res.Error = fmt.Sprintf(format, args...)
		return res
	}
	if o.Sudo && o.RemoteBinary == "" {
		return fail("--sudo requires --remote-binary: a binary copied into the ssh user's own " +
			"cache directory must never be the one a sudo rule allows to run as root, since that " +
			"user could replace it")
	}
	t, err := parseHost(host)
	if err != nil {
		return fail("%v", err)
	}

	// 1. Architecture and home directory.
	out, err := run(ctx, rn, o.ssh(t, `uname -m && printf '%s\n' "$HOME"`, nil))
	if err != nil {
		return fail("connection: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return fail("unexpected answer to uname: %q", out)
	}
	res.Arch = strings.TrimSpace(lines[0])
	home := strings.TrimSpace(lines[1])
	cache := home + "/.cache/terminus"

	// 2. The binary to run: installed, or copied (once per content).
	bin := o.RemoteBinary
	if bin == "" {
		local, err := o.localBinary(res.Arch)
		if err != nil {
			return fail("%v", err)
		}
		sum, err := fileHash(local)
		if err != nil {
			return fail("%v", err)
		}
		bin = cache + "/terminus-" + sum[:16]
		copied, err := upload(ctx, rn, o, t, local, bin, sum)
		if err != nil {
			return fail("copy: %v", err)
		}
		res.Copied = copied
	}

	cmd := []string{bin, "report", "-o", "json", "--color", "never"}
	if o.RemoteConfig != "" {
		sum, err := fileHash(o.RemoteConfig)
		if err != nil {
			return fail("%v", err)
		}
		cfg := cache + "/config-" + sum[:16] + ".toml"
		if _, err := upload(ctx, rn, o, t, o.RemoteConfig, cfg, sum); err != nil {
			return fail("copy of the configuration: %v", err)
		}
		cmd = append(cmd, "--config", cfg)
	}
	cmd = append(cmd, o.Args...)
	if o.Sudo {
		cmd = append([]string{"sudo", "-n"}, cmd...)
	}

	// 3. The report. Exit codes 0-2 carry a report; 3 is an error of the remote terminus.
	r, err := rn.Run(ctx, o.ssh(t, shellJoin(cmd), nil))
	if err != nil {
		return fail("run: %v", err)
	}
	switch r.ExitCode {
	case model.ExitOK, model.ExitWarn, model.ExitFail:
	case 255:
		return fail("ssh: %s", strings.TrimSpace(string(r.Stderr)))
	default:
		return fail("remote terminus exit code %d: %s", r.ExitCode, strings.TrimSpace(string(r.Stderr)))
	}
	var rep model.Report
	if err := json.Unmarshal(r.Stdout, &rep); err != nil {
		return fail("invalid report: %v (stderr: %s)", err, strings.TrimSpace(string(r.Stderr)))
	}
	res.Report = &rep
	return res
}

// run runs a command that must succeed and returns its output.
func run(ctx context.Context, rn runner.Runner, c runner.Cmd) ([]byte, error) {
	r, err := rn.Run(ctx, c)
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 {
		msg := strings.TrimSpace(string(r.Stderr))
		if msg == "" {
			msg = fmt.Sprintf("exit code %d", r.ExitCode)
		}
		return r.Stdout, errors.New(msg)
	}
	return r.Stdout, nil
}

// upload copies a local file to path on the host unless a file with the same hash is there.
func upload(ctx context.Context, rn runner.Runner, o Options, t target, local, path, sum string) (bool, error) {
	check, err := rn.Run(ctx, o.ssh(t, "sha256sum "+shellQuote(path)+" 2>/dev/null", nil))
	if err == nil && check.ExitCode == 0 && strings.HasPrefix(string(check.Stdout), sum) {
		return false, nil
	}
	f, err := os.Open(local)
	if err != nil {
		return false, err
	}
	defer f.Close()
	tmp := path + ".tmp"
	script := fmt.Sprintf("mkdir -p %s && cat > %s && chmod 755 %s && mv %s %s",
		shellQuote(filepath.Dir(path)), shellQuote(tmp), shellQuote(tmp), shellQuote(tmp), shellQuote(path))
	if _, err := run(ctx, rn, o.ssh(t, script, f)); err != nil {
		return false, err
	}
	return true, nil
}

// localBinary finds the terminus binary for a remote architecture.
func (o Options) localBinary(unameArch string) (string, error) {
	arch, ok := archs[unameArch]
	if !ok {
		return "", fmt.Errorf("unsupported remote architecture %q (supported: x86_64, aarch64)", unameArch)
	}
	exe, err := o.Executable()
	if err != nil {
		return "", err
	}
	if arch == o.Arch {
		return exe, nil
	}
	name := "terminus-linux-" + arch
	dirs := []string{filepath.Dir(exe)}
	if o.BinariesDir != "" {
		dirs = append([]string{o.BinariesDir}, dirs...)
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("the host is %s: put %s in --binaries-dir (or next to terminus), or use --remote-binary", unameArch, name)
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// shellQuote quotes a word for the remote POSIX shell.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,@%+", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoin(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = shellQuote(w)
	}
	return strings.Join(q, " ")
}

// ReadHostsFile reads one host per line, ignoring blank lines and # comments.
func ReadHostsFile(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var hosts []string
	for _, l := range bytes.Split(b, []byte("\n")) {
		s := strings.TrimSpace(string(l))
		if i := strings.Index(s, "#"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		if s != "" {
			hosts = append(hosts, s)
		}
	}
	return hosts, nil
}
