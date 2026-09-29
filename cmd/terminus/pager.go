package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// page writes what write produces to stdout, through a pager when stdout is a terminal and the
// output does not fit in it (as git and systemctl do). The pager is $TERMINUS_PAGER, $PAGER or
// less; an empty value or "cat" turns it off, as does --no-pager.
func page(g *globalOptions, stdout io.Writer, write func(io.Writer) error) error {
	f, ok := stdout.(*os.File)
	if g.noPager || !ok || !term.IsTerminal(int(f.Fd())) {
		return write(stdout)
	}
	var buf bytes.Buffer
	if err := write(&buf); err != nil {
		return err
	}
	_, height, err := term.GetSize(int(f.Fd()))
	pager := pagerCommand()
	if err != nil || bytes.Count(buf.Bytes(), []byte("\n")) < height || len(pager) == 0 {
		_, err := f.Write(buf.Bytes())
		return err
	}

	cmd := exec.Command(pager[0], pager[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = &buf, f, os.Stderr
	cmd.Env = os.Environ()
	if _, set := os.LookupEnv("LESS"); !set {
		// Quit when the output fits, keep the colors, leave the output on the screen.
		cmd.Env = append(cmd.Env, "LESS=FRX")
	}
	if os.Geteuid() == 0 {
		// No shell escapes or file opening from a pager running as root (as systemctl does).
		cmd.Env = append(cmd.Env, "LESSSECURE=1")
	}
	if err := cmd.Start(); err != nil {
		// No pager on this machine: print as is.
		_, err := f.Write(buf.Bytes())
		return err
	}
	// Quitting the pager early is not an error.
	_ = cmd.Wait()
	return nil
}

func pagerCommand() []string {
	p, set := os.LookupEnv("TERMINUS_PAGER")
	if !set {
		p, set = os.LookupEnv("PAGER")
	}
	if !set {
		p = "less"
	}
	fields := strings.Fields(p)
	if len(fields) == 0 || fields[0] == "cat" {
		return nil
	}
	return fields
}
