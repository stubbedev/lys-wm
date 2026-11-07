// Command lys-wmctl queries and controls a running lys-wm over its IPC socket.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const usage = `usage: lys-wmctl [--socket <path>] <command>

  dispatch <action> [args...]  run an action, as a keybind would (see docs/actions.md)
  batch <action>...            run each argument as an action, in order and at once,
                               stopping at the first that fails
  reload                       read the config file again; prints what it did not apply
  upgrade [--drop-x11] <path>  move this session to the lys-wm at path, keeping its clients; refused and unchanged if any client cannot be kept
  configproblems               print what the last load of the config did not apply as JSON
  outputs                      print the outputs as JSON
  workspaces                   print the open workspaces as JSON
  windows                      print the mapped windows as JSON
  activewindow                 print the focused window as JSON, or null
  submap                       print the bind submap as JSON, "" for the default binds
  layout                       print the keyboard layout group as JSON
  locks                        print the Caps, Num and Scroll Lock state as JSON
  reducedmotion                print whether animations.reduced_motion is on as JSON
  version                      print the IPC protocol version
  layers                       print the mapped layer surfaces (bars, wallpapers) as JSON
  devices                      print the input devices as JSON
  pickwindow                   wait for a click on a window and print its ID, or null;
                               Escape cancels
  watch [--snapshot]           print events as JSON lines until lys-wm exits; with
                               --snapshot, the current state first, as events
  icon <window> <size>         print a window's icon buffer closest to size pixels
                               as JSON, the PNG base64-encoded
  output create [--name <name>] [--scale <s>] <width>x<height>[@<hz>]
                               add a virtual output, at 60 Hz without @<hz>, and
                               print its name as JSON
  output remove <name>         remove a virtual output; its workspaces move as
                               when a monitor is unplugged

The socket is --socket, else $LYS_WM_SOCKET, else the only socket in
$XDG_RUNTIME_DIR/lys-wm. The exit status is 3 when no lys-wm answers there,
and 1 for any other failure.
`

const (
	exitFailed     = 1
	exitNotRunning = 3
)

func exitCode(err error) int {
	if errors.Is(err, ipc.ErrNoSocket) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return exitNotRunning
	}
	return exitFailed
}

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lys-wmctl:", err)
		os.Exit(exitCode(err))
	}
}

var errUsage = errors.New("bad usage")

var queries = []ipc.Kind{ipc.KindOutputs, ipc.KindWorkspaces, ipc.KindWindows, ipc.KindActiveWindow, ipc.KindSubmap, ipc.KindLayout, ipc.KindLocks, ipc.KindReducedMotion, ipc.KindOverview, ipc.KindScratchpads, ipc.KindConfigProblems, ipc.KindVersion, ipc.KindLayers, ipc.KindDevices, ipc.KindPickWindow}

func parseArgs(args []string) (ipc.Request, error) {
	if len(args) == 0 {
		return ipc.Request{}, fmt.Errorf("%w: no command", errUsage)
	}
	cmd, rest := args[0], args[1:]
	var req ipc.Request
	switch kind := ipc.Kind(cmd); {
	case kind == ipc.KindDispatch:
		if len(rest) == 0 {
			return ipc.Request{}, fmt.Errorf("%w: dispatch needs an action", errUsage)
		}
		return ipc.Request{Request: ipc.KindDispatch, Action: strings.Join(rest, " ")}, nil
	case cmd == "batch":
		if len(rest) == 0 {
			return ipc.Request{}, fmt.Errorf("%w: batch needs at least one action", errUsage)
		}
		return ipc.Request{Request: ipc.KindDispatch, Actions: rest}, nil
	case kind == ipc.KindWatch:
		switch {
		case len(rest) == 0:
			return ipc.Request{Request: ipc.KindWatch}, nil
		case len(rest) == 1 && rest[0] == "--snapshot":
			return ipc.Request{Request: ipc.KindWatch, Snapshot: true}, nil
		}
		return ipc.Request{}, fmt.Errorf("%w: watch takes only --snapshot", errUsage)
	case kind == ipc.KindIcon:
		return parseIcon(rest)
	case kind == ipc.KindUpgrade:
		return parseUpgrade(rest)
	case cmd == "output":
		return parseOutput(rest)
	case cmd == "reload":
		req = ipc.Request{Request: ipc.KindDispatch, Action: wm.ReloadAction{}.String()}
	case slices.Contains(queries, kind):
		req = ipc.Request{Request: kind}
	default:
		return ipc.Request{}, fmt.Errorf("%w: unknown command %q", errUsage, cmd)
	}
	if len(rest) != 0 {
		return ipc.Request{}, fmt.Errorf("%w: %s takes no arguments", errUsage, cmd)
	}
	return req, nil
}

const defaultRefreshMHz = 60000

func parseOutput(args []string) (ipc.Request, error) {
	if len(args) == 0 {
		return ipc.Request{}, fmt.Errorf("%w: output takes create or remove", errUsage)
	}
	switch verb, rest := args[0], args[1:]; verb {
	case "create":
		return parseCreateOutput(rest)
	case "remove":
		if len(rest) != 1 {
			return ipc.Request{}, fmt.Errorf("%w: output remove takes the name of a virtual output", errUsage)
		}
		return ipc.Request{Request: ipc.KindRemoveOutput, Name: rest[0]}, nil
	default:
		return ipc.Request{}, fmt.Errorf("%w: output takes create or remove, not %q", errUsage, verb)
	}
}

func parseCreateOutput(args []string) (ipc.Request, error) {
	flags := flag.NewFlagSet("output create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "")
	scale := flags.Float64("scale", 0, "")
	if err := flags.Parse(args); err != nil {
		return ipc.Request{}, fmt.Errorf("%w: output create: %w", errUsage, err)
	}
	if flags.NArg() != 1 {
		return ipc.Request{}, fmt.Errorf("%w: output create takes one <width>x<height>[@<hz>]", errUsage)
	}
	scaled := false
	flags.Visit(func(f *flag.Flag) { scaled = scaled || f.Name == "scale" })
	if scaled && !(*scale > 0) {
		return ipc.Request{}, fmt.Errorf("%w: output create: scale %g is not above 0", errUsage, *scale)
	}
	m, err := ipc.ParseMode(flags.Arg(0))
	if err != nil {
		return ipc.Request{}, fmt.Errorf("%w: output create: %w", errUsage, err)
	}
	if m.RefreshMHz == 0 {
		m.RefreshMHz = defaultRefreshMHz
	}
	return ipc.Request{Request: ipc.KindCreateOutput, Name: *name, Mode: &m, Scale: *scale}, nil
}

func parseUpgrade(args []string) (ipc.Request, error) {
	req := ipc.Request{Request: ipc.KindUpgrade}
	if len(args) > 0 && args[0] == "--drop-x11" {
		req.DropX11 = true
		args = args[1:]
	}
	if len(args) != 1 {
		return ipc.Request{}, fmt.Errorf("%w: upgrade takes the path of the new lys-wm", errUsage)
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return ipc.Request{}, fmt.Errorf("find the new lys-wm: %w", err)
	}
	if req.Binary, err = filepath.Abs(path); err != nil {
		return ipc.Request{}, fmt.Errorf("find the new lys-wm: %w", err)
	}
	return req, nil
}

func parseIcon(args []string) (ipc.Request, error) {
	if len(args) != 2 {
		return ipc.Request{}, fmt.Errorf("%w: icon takes a window ID and a size", errUsage)
	}
	id, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil || id == 0 {
		return ipc.Request{}, fmt.Errorf("%w: window ID %q is not a positive number", errUsage, args[0])
	}
	size, err := strconv.Atoi(args[1])
	if err != nil || size <= 0 {
		return ipc.Request{}, fmt.Errorf("%w: icon size %q is not a positive number", errUsage, args[1])
	}
	return ipc.Request{Request: ipc.KindIcon, Window: wm.WindowID(id), Size: size}, nil
}

func watch(path string, snapshot bool, out io.Writer) (err error) {
	stream, err := ipc.Subscribe(path, snapshot)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	for {
		line, err := stream.Next()
		if err != nil {
			return err
		}
		if _, err := out.Write(line); err != nil {
			return err
		}
	}
}

func printWarnings(path string, warnings io.Writer) error {
	result, err := ipc.Do(path, ipc.Request{Request: ipc.KindConfigProblems})
	if err != nil {
		return fmt.Errorf("ask what the reload did not apply: %w", err)
	}
	var problems ipc.ConfigProblems
	if err := json.Unmarshal(result, &problems); err != nil {
		return fmt.Errorf("read what the reload did not apply: %w", err)
	}
	for _, m := range problems.Messages {
		if _, err := fmt.Fprintln(warnings, "lys-wmctl: warning:", m); err != nil {
			return err
		}
	}
	return nil
}

func run(args []string, getenv func(string) string, out, warnings io.Writer) error {
	flags := flag.NewFlagSet("lys-wmctl", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	req, err := parseArgs(flags.Args())
	if err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	path := *socket
	if path == "" {
		if path, err = ipc.FindSocket(getenv); err != nil {
			return err
		}
	}
	if req.Request == ipc.KindWatch {
		return watch(path, req.Snapshot, out)
	}
	result, err := ipc.Do(path, req)
	if err != nil {
		return err
	}
	if req.Action == (wm.ReloadAction{}).String() {
		return printWarnings(path, warnings)
	}
	if req.Request == ipc.KindDispatch || req.Request == ipc.KindRemoveOutput {
		return nil
	}
	if result == nil {
		result = json.RawMessage("null")
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, result, "", "  "); err != nil {
		return fmt.Errorf("format the result: %w", err)
	}
	pretty.WriteByte('\n')
	_, err = out.Write(pretty.Bytes())
	return err
}
