//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

const (
	builderCPUs      = 4
	builderMemoryMiB = 4096
	agentUID         = 1000
)

// builderStateDir is the builder of a workspace: shared by all workspaces,
// or the workspace's own with isolated_build.
func builderStateDir(ws *workspace.Resolved) string {
	if ws.IsolatedBuild {
		return filepath.Join(vmStateDir(ws.Name), "builder")
	}
	return filepath.Join(vmRootDir(), "builder")
}

// runUpVM builds the workspace image in a builder VM and boots it as the
// workspace VM.
func runUpVM(ctx context.Context, cfg *config.Config, ws *workspace.Resolved) error {
	stateDir := vmStateDir(ws.Name)
	if st, err := runningVM(stateDir); err != nil {
		return err
	} else if st != nil {
		_, _ = color.New(color.FgYellow).Printf("Workspace %s is already running\n", ws.Name)
		return nil
	}

	// Must run before every generated-file write below.
	if err := validateSecretSources(ws); err != nil {
		return fmt.Errorf("validate secrets for workspace %q: %w", ws.Name, err)
	}
	if err := materializeCABundle(ws); err != nil {
		return fmt.Errorf("prepare CA bundle for workspace %q: %w", ws.Name, err)
	}
	if hostPath, ok := compose.ReadOnlyMountCoversPath(ws.Mounts, ocConfigContainerPath); ok {
		if err := ensureOCConfigGitignore(hostPath); err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: could not pre-create .gitignore in %s: %v\n", hostPath, err)
		}
	}
	cacheDir := filepath.Clean(ComposeCacheDir(ws.Name))
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return fmt.Errorf("create cache directory %q: %w", cacheDir, err)
	}
	if err := writeTUIConfig(cacheDir); err != nil {
		return err
	}
	shares, err := vmShares(ws, cacheDir)
	if err != nil {
		return err
	}
	copies, err := vmCopies(ws)
	if err != nil {
		return err
	}
	if err := checkCopies(ws, copies, shares); err != nil {
		return err
	}

	rt, err := vmRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	ref, err := buildWorkspaceImage(ctx, cfg, ws, rt)
	if err != nil {
		return err
	}

	stagedDir, binds, err := stageCopies(stateDir, copies)
	if err != nil {
		return fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	if stagedDir != "" {
		shares = append(shares, vm.Share{HostPath: stagedDir, GuestPath: guest.CopiesDir, ReadOnly: true})
	}
	spec, err := workspaceSpec(ctx, ws, rt, ref, shares, binds)
	if err != nil {
		return err
	}
	if _, err := startSupervisor(ctx, spec, "VM for workspace "+ws.Name); err != nil {
		return fmt.Errorf("start vm for workspace %q: %w", ws.Name, err)
	}
	if err := pushSecrets(ctx, ws); err != nil {
		_ = stopSupervisor(stateDir)
		return fmt.Errorf("deliver secrets to workspace %q: %w", ws.Name, err)
	}
	_, _ = color.New(color.FgGreen).Printf("Workspace %s started\n", ws.Name)
	return nil
}

// buildWorkspaceImage runs the image cascade in a builder VM and unpacks the
// result into the image cache. It returns the image-cache reference.
func buildWorkspaceImage(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, rt vmRuntimeFiles) (string, error) {
	dir := builderStateDir(ws)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create builder state dir: %w", err)
	}
	lock, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return "", fmt.Errorf("open builder lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // G115: file descriptors fit in int
		return "", fmt.Errorf("lock builder: %w", err)
	}

	policy := []byte(`{}`)
	label := "shared image builder VM"
	if ws.IsolatedBuild {
		if policy, err = vmPolicy(ctx, ws); err != nil {
			return "", err
		}
		label = "image builder VM for workspace " + ws.Name
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate jailoc executable: %w", err)
	}
	spec := vm.Spec{
		Kind:       vm.KindBuilder,
		Workspace:  "builder",
		StateDir:   dir,
		Image:      vm.DefaultImage,
		CPUs:       builderCPUs,
		MemoryMiB:  builderMemoryMiB,
		DiskSize:   vm.DefaultDiskSize,
		Policy:     policy,
		RunnerPath: rt.runner,
		LibDir:     rt.libDir,
		ImageCache: filepath.Join(vmRootDir(), "images"),
		Broker:     []string{exe, "netbroker"},
	}
	if ws.IsolatedBuild {
		spec.Workspace = ws.Name + "-builder"
	}
	st, err := startSupervisor(ctx, spec, label)
	if err != nil {
		return "", fmt.Errorf("start image builder: %w", err)
	}
	defer func() {
		if err := stopSupervisor(dir); err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: stop image builder: %v\n", err)
		}
	}()
	if err := setDockerHost(st.DockerSocket); err != nil {
		return "", err
	}

	_, _ = color.New(color.FgCyan).Printf("Resolving image for workspace %s...\n", ws.Name)
	finalImage, err := ResolveAndLayerImage(ctx, cfg, ws, appVersion)
	if err != nil {
		return "", fmt.Errorf("resolve image for workspace %q: %w", ws.Name, err)
	}
	id, err := docker.ImageID(ctx, finalImage)
	if err != nil {
		return "", err
	}
	ref := vm.RootFSRef(id)
	err = vm.ImportImage(ctx, filepath.Join(vmRootDir(), "images"), ref, func(w io.Writer) error {
		_, _ = color.New(color.FgCyan).Printf("Unpacking image %s for the workspace VM...\n", finalImage)
		_, err := docker.SaveImage(ctx, finalImage, w)
		return err
	})
	if err != nil {
		return "", err
	}
	return ref, nil
}

// workspaceSpec describes the workspace VM booting ref.
func workspaceSpec(ctx context.Context, ws *workspace.Resolved, rt vmRuntimeFiles, ref string, shares []vm.Share, copies []guest.Copy) (vm.Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return vm.Spec{}, fmt.Errorf("locate jailoc executable: %w", err)
	}
	memMiB, err := vmMemoryMiB(ws.Memory)
	if err != nil {
		return vm.Spec{}, err
	}
	policy, err := vmPolicy(ctx, ws)
	if err != nil {
		return vm.Spec{}, err
	}
	g := &guest.Config{
		Hostname: ws.Name,
		User:     "agent",
		UID:      agentUID,
		GID:      agentUID,
		Home:     "/home/agent",
		WorkDir:  ws.Paths[0],
		Copies:   copies,
		Env: []string{
			"JAILOC=1",
			"JAILOC_WORKSPACE=" + ws.Name,
			"OPENCODE_TUI_CONFIG=" + filepath.Join(guestCacheDir, "tui-container.json"),
		},
	}
	if _, err := os.Stat(filepath.Join(config.ConfigDir(), "workspaces", ws.Name, caBundleFilename)); err == nil {
		g.CABundle = filepath.Join(guestConfigDir, caBundleFilename)
	}
	return vm.Spec{
		Kind:        vm.KindWorkspace,
		Workspace:   ws.Name,
		StateDir:    vmStateDir(ws.Name),
		Image:       ref,
		CPUs:        uint32(math.Ceil(ws.CPU)),
		MemoryMiB:   memMiB,
		Shares:      shares,
		Policy:      policy,
		RunnerPath:  rt.runner,
		LibDir:      rt.libDir,
		ImageCache:  filepath.Join(vmRootDir(), "images"),
		Broker:      []string{exe, "netbroker"},
		GuestBinary: rt.guest,
		Guest:       g,
	}, nil
}

// pushSecrets delivers the workspace environment and secrets to the guest
// over SSH; they live only in guest memory.
func pushSecrets(ctx context.Context, ws *workspace.Resolved) error {
	payload := guest.Secrets{Env: append([]string{}, ws.Env...), Files: map[string][]byte{}}
	for _, s := range ws.Secrets {
		var value []byte
		if s.FromEnv != "" {
			value = []byte(os.Getenv(s.FromEnv))
		} else {
			data, err := os.ReadFile(s.FromFile)
			if err != nil {
				return fmt.Errorf("read secret %q: %w", s.Name, err)
			}
			value = data
		}
		payload.Files[s.Name] = value
		if s.Kind == workspace.SecretKindEnv {
			payload.Env = append(payload.Env, s.Name+"="+string(value))
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	client, err := vm.DialSSH(ctx, vmStateDir(ws.Name))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	sess.Stdin = strings.NewReader(string(data))
	var stderr strings.Builder
	sess.Stderr = &stderr
	if err := sess.Run(guest.BinaryPath + " secrets"); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// runSessionVM starts the workspace if needed and runs the OpenCode TUI in
// it over SSH, attached to this terminal.
func runSessionVM(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, dir, session string, cont bool) error {
	if st, err := runningVM(vmStateDir(ws.Name)); err != nil {
		return err
	} else if st == nil {
		workspaceExplicit = true
		if err := runUpVM(ctx, cfg, ws); err != nil {
			return err
		}
	}
	client, err := vm.DialSSH(ctx, vmStateDir(ws.Name))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	cmdline := []string{guest.BinaryPath, "run", "-C", dir}
	for _, kv := range terminalEnv() {
		cmdline = append(cmdline, "-e", kv)
	}
	cmdline = append(cmdline, "--", "opencode")
	if session != "" {
		cmdline = append(cmdline, "--session", session)
	}
	if cont {
		cmdline = append(cmdline, "--continue")
	}

	fd := int(os.Stdin.Fd()) //nolint:gosec // G115: file descriptors fit in int
	if term.IsTerminal(fd) {
		w, h, err := term.GetSize(fd)
		if err != nil {
			w, h = 80, 24
		}
		termName := os.Getenv("TERM")
		if termName == "" {
			termName = "xterm-256color"
		}
		if err := sess.RequestPty(termName, h, w, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
			return fmt.Errorf("request pty: %w", err)
		}
		old, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("set terminal raw mode: %w", err)
		}
		defer func() { _ = term.Restore(fd, old) }()
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				if w, h, err := term.GetSize(fd); err == nil {
					_ = sess.WindowChange(h, w)
				}
			}
		}()
	}
	sess.Stdin, sess.Stdout, sess.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = sess.Run(shellJoin(cmdline))
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("opencode exited with status %d", exitErr.ExitStatus())
	}
	if err != nil {
		return fmt.Errorf("run opencode in workspace %q: %w", ws.Name, err)
	}
	return nil
}

// shellJoin quotes args for the guest's /bin/sh -c.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// downVM stops the workspace VM; purge also deletes its state, persistent
// home directories and isolated builder.
func downVM(ws *workspace.Resolved, purge bool) error {
	stateDir := vmStateDir(ws.Name)
	st, err := runningVM(stateDir)
	if err != nil {
		return err
	}
	if st == nil && !purge {
		_, _ = color.New(color.FgYellow).Printf("Workspace %s is not running\n", ws.Name)
		return nil
	}
	if st != nil {
		_, _ = color.New(color.FgCyan).Printf("Stopping workspace %s...\n", ws.Name)
	}
	if err := stopSupervisor(stateDir); err != nil {
		return err
	}
	if err := stopSupervisor(filepath.Join(stateDir, "builder")); err != nil {
		return err
	}
	if purge {
		if err := os.RemoveAll(stateDir); err != nil {
			return fmt.Errorf("delete vm state: %w", err)
		}
		_, _ = color.New(color.FgGreen).Printf("Workspace %s VM deleted\n", ws.Name)
		return nil
	}
	_, _ = color.New(color.FgGreen).Printf("Workspace %s stopped\n", ws.Name)
	return nil
}

// statusVM prints whether the workspace VM runs.
func statusVM(ws *workspace.Resolved) error {
	st, err := runningVM(vmStateDir(ws.Name))
	if err != nil {
		return err
	}
	fmt.Printf("Workspace: %s\n", ws.Name)
	fmt.Printf("Backend:   vm\n")
	if st == nil {
		fmt.Printf("State:     stopped\n")
		return nil
	}
	fmt.Printf("State:     running (supervisor pid %d)\n", st.PID)
	fmt.Printf("Started:   %s\n", st.Updated.Local().Format(time.RFC3339))
	return nil
}

// logsVM prints the supervisor and guest console logs; follow keeps
// printing the console log as it grows.
func logsVM(ctx context.Context, ws *workspace.Resolved, follow bool, w io.Writer) error {
	stateDir := vmStateDir(ws.Name)
	console := filepath.Join(stateDir, "microvm", "console.log")
	if _, err := os.Stat(console); err != nil {
		return fmt.Errorf("workspace %q has no VM logs; start it with 'jailoc up'", ws.Name)
	}
	if data, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil { //nolint:gosec // G304: jailoc-owned state path
		_, _ = w.Write(data)
	}
	f, err := os.Open(console) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open console log: %w", err)
	}
	defer func() { _ = f.Close() }()
	for {
		if _, err := io.Copy(w, f); err != nil {
			return fmt.Errorf("read console log: %w", err)
		}
		if !follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// restartVMIfRunning restarts a running workspace VM so configuration
// changes (e.g. new paths) take effect.
func restartVMIfRunning(ctx context.Context, ws *workspace.Resolved) error {
	if st, err := runningVM(vmStateDir(ws.Name)); err != nil || st == nil {
		return err
	}
	if err := downVM(ws, false); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("reload config for restart: %w", err)
	}
	ws2, err := workspace.Resolve(cfg, ws.Name)
	if err != nil {
		return fmt.Errorf("re-resolve workspace for restart: %w", err)
	}
	return runUpVM(ctx, cfg, ws2)
}
