package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mayahiro/process-deck/internal/config"
	"github.com/mayahiro/process-deck/internal/supervisor"
)

type bootstrap struct {
	Socket  string
	Config  *config.Config
	Options supervisor.Options
}

// Open reconnects to the session for a canonical working directory and config
// path, or starts a detached supervisor if no session exists. An existing
// session retains its original config, environment, and exit-when-done mode.
func Open(configPath, baseDir string, keepRunningWhenDone bool) (*Client, error) {
	path, err := socketPath(configPath, baseDir)
	if err != nil {
		return nil, err
	}
	return openAt(path, configPath, baseDir, keepRunningWhenDone)
}

func openAt(path, configPath, baseDir string, keepRunningWhenDone bool) (*Client, error) {
	// Keep the lock file in place, including after shutdown. Unlinking a
	// flock file could let concurrent openers lock different inodes.
	fd, err := syscall.Open(path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("runtime error: cannot open session lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), path+".lock")
	defer lock.Close()
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("runtime error: cannot lock session: %w", err)
		}
		// The owner may still be booting. Never spawn a second supervisor.
		deadline := time.Now().Add(connectTimeout)
		for {
			client, err := connect(path)
			if err == nil {
				return client, nil
			}
			if (!errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ECONNREFUSED)) || time.Now().After(deadline) {
				return nil, err
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("runtime error: session supervisor is unavailable; processes may still be running; stop them before removing the stale socket %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := supervisor.ValidateGraph(cfg.DependencyMap()); err != nil {
		return nil, err
	}
	options := supervisor.Options{BaseDir: baseDir, KeepRunningWhenDone: keepRunningWhenDone}
	if err := launch(lock, bootstrap{Socket: path, Config: cfg, Options: options}); err != nil {
		return nil, err
	}
	return connect(path)
}

func socketPath(configPath, baseDir string) (string, error) {
	baseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	baseDir, err = filepath.EvalSymlinks(baseDir)
	if err != nil {
		return "", err
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(configPath); err == nil {
		configPath = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// macOS has a short sockaddr_un path limit. Its per-user TMPDIR can
	// already consume most of it, so use a short, owner-only directory.
	dir := filepath.Join("/tmp", fmt.Sprintf("procdeck-%d", os.Getuid()))
	if err := privateDirectory(dir); err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(baseDir + "\x00" + configPath))
	return filepath.Join(dir, fmt.Sprintf("%x.sock", key[:16])), nil
}

func privateDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("runtime error: cannot create session directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("runtime error: session directory must be owned by the current user with mode 0700: %s", path)
	}
	return nil
}

func launch(lock *os.File, init bootstrap) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	input, configWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	defer input.Close()
	defer configWriter.Close()
	ready, output, err := os.Pipe()
	if err != nil {
		return err
	}
	defer ready.Close()
	defer output.Close()

	cmd := exec.Command(executable, workerArgument)
	cmd.Dir = init.Options.BaseDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Config (including explicit env values) travels over an inherited pipe,
	// never argv or a state file. Standard I/O is disconnected from the UI.
	cmd.ExtraFiles = []*os.File{lock, input, output}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("runtime error: cannot start session supervisor: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	_ = input.Close()
	_ = output.Close()
	_ = configWriter.SetWriteDeadline(time.Now().Add(connectTimeout))
	if err := json.NewEncoder(configWriter).Encode(init); err != nil {
		return fmt.Errorf("runtime error: cannot initialize session supervisor: %w", err)
	}
	_ = configWriter.Close()
	_ = ready.SetReadDeadline(time.Now().Add(connectTimeout))
	var reply response
	if err := json.NewDecoder(ready).Decode(&reply); err != nil {
		return fmt.Errorf("runtime error: session startup did not complete; retry procdeck to reconnect: %w", err)
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	return nil
}

// IsWorker reports whether args select procdeck's private supervisor entrypoint.
func IsWorker(args []string) bool {
	return len(args) == 1 && args[0] == workerArgument
}

// RunWorker serves the private session described by inherited descriptors 3-5.
// It must only be called by the executable's worker entrypoint.
func RunWorker() error {
	lock := os.NewFile(3, "session-lock")
	input := os.NewFile(4, "session-config")
	output := os.NewFile(5, "session-ready")
	defer lock.Close()
	defer input.Close()
	defer output.Close()
	for _, file := range []*os.File{lock, input, output} {
		if _, err := file.Stat(); err != nil {
			return fmt.Errorf("runtime error: session worker requires inherited descriptors")
		}
		syscall.CloseOnExec(int(file.Fd()))
	}
	var init bootstrap
	if err := json.NewDecoder(input).Decode(&init); err != nil {
		return err
	}
	_ = input.Close()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: init.Socket, Net: "unix"})
	if err != nil {
		_ = json.NewEncoder(output).Encode(response{Error: errorText(err)})
		return err
	}
	defer listener.Close()
	if err := os.Chmod(init.Socket, 0600); err != nil {
		_ = json.NewEncoder(output).Encode(response{Error: errorText(err)})
		return err
	}
	if err := json.NewEncoder(output).Encode(response{Version: protocolVersion}); err != nil {
		return err
	}
	_ = output.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, listener, init.Config, init.Options)
}
