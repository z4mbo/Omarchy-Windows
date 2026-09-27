//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Each experimental launch reserves its own directory before QEMU starts.
// It never borrows a normal launcher's QMP socket or a TCP listener reachable
// from the guest. The directory identity is retained to reject replacement.
type experimentalBalloonControl struct {
	dir, path string
	identity  os.FileInfo
}

func prepareExperimentalBalloonControl() (*experimentalBalloonControl, error) {
	base, err := platformQMPControlDirectory()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(base), "OmarchyRAM-")
	if err != nil {
		return nil, err
	}
	control := &experimentalBalloonControl{dir: dir, path: filepath.Join(dir, "balloon.sock")}
	control.identity, err = os.Lstat(dir)
	if err == nil {
		_, err = control.validatedPath()
	}
	if err != nil {
		_ = os.Remove(dir) // our newly created, empty directory only
		return nil, err
	}
	return control, nil
}

func (c *experimentalBalloonControl) validatedPath() (string, error) {
	if c == nil || c.identity == nil || !filepath.IsAbs(c.dir) ||
		c.path != filepath.Join(c.dir, "balloon.sock") || len([]byte(c.path)) > 103 {
		return "", fmt.Errorf("invalid experimental memory control endpoint")
	}
	if err := validateMovePath(c.dir); err != nil {
		return "", err
	}
	current, err := os.Lstat(c.dir)
	if err != nil {
		return "", err
	}
	if !current.IsDir() || !os.SameFile(c.identity, current) {
		return "", fmt.Errorf("experimental memory control directory changed")
	}
	return c.path, nil
}

// Close is called after the caller has stopped its child QEMU. Refuse to
// remove a still-listening endpoint, a replaced directory, or an ordinary
// file. All removals are individual entries in our reserved directory.
func (c *experimentalBalloonControl) Close() error {
	if c == nil {
		return nil
	}
	if _, err := os.Lstat(c.dir); os.IsNotExist(err) {
		return nil
	}
	path, err := c.validatedPath()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if !isQMPControlSocket(path) {
			return fmt.Errorf("experimental memory endpoint contains another file")
		}
		conn, err := net.DialTimeout("unix", path, 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return fmt.Errorf("experimental memory endpoint is still in use")
		}
		if !qmpConnectionRefused(err) && !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect experimental memory endpoint: %w", err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Remove(c.dir)
}
