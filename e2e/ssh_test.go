// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestReceiveOverSSHForcedCommand exercises `demiplane receive` exactly as it
// runs in production: as an OpenSSH forced command (`command="..."` in
// authorized_keys, pubkey auth, SSH_ORIGINAL_COMMAND ignored) rather than
// invoked directly. It spins up a real, unprivileged `sshd` on an ephemeral
// loopback port with its own throwaway host key and a single authorized
// client key, pipes an artifact over `ssh ... < file`, then starts a real
// `demiplane serve` against the SAME store directory to confirm the
// SSH-received artifact is actually readable back over HTTP.
//
// This is best-effort by design (per the mission spec): unprivileged sshd has
// enough environment-specific footguns (privilege separation, PAM, missing
// system directories) that a hard requirement here would make the suite flaky
// on exactly the CI runners it's meant to protect. Any setup failure is a
// clean, explained Skip — never a flaky Fail.
func TestReceiveOverSSHForcedCommand(t *testing.T) {
	sshdPath, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("skip: no sshd on PATH")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("skip: no ssh-keygen on PATH")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("skip: no ssh client on PATH")
	}

	dir := t.TempDir()
	hostKey := filepath.Join(dir, "hostkey")
	clientKey := filepath.Join(dir, "clientkey")
	authorizedKeys := filepath.Join(dir, "authorized_keys")
	storeDir := filepath.Join(dir, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}

	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", hostKey, "-N", "", "-q").CombinedOutput(); err != nil {
		t.Skipf("skip: ssh-keygen host key failed: %v\n%s", err, out)
	}
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", clientKey, "-N", "", "-q").CombinedOutput(); err != nil {
		t.Skipf("skip: ssh-keygen client key failed: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(clientKey + ".pub")
	if err != nil {
		t.Skipf("skip: read client pubkey: %v", err)
	}

	baseURL := "http://ssh-receive.example"
	// The forced command: exactly the shape documented in `demiplane help` for
	// an authorized_keys pubkey-auth publisher. restrict disables everything
	// SSH offers beyond running this one command (no port-forwarding, no pty,
	// no agent forwarding) — the production-recommended posture.
	//
	// The command= VALUE is quoted per sshd's authorized_keys option syntax
	// (double quotes, backslash-escaped — see sshd(8) AUTHORIZED_KEYS FILE
	// FORMAT), which is NOT POSIX shell quoting. Single-quoting it (an earlier
	// version of this test did) is silently misparsed: sshd stops the value at
	// the first space since single quotes mean nothing to it, truncates the
	// command to `'demiplane`, and rejects the whole key line — which surfaces
	// to the client as a generic, misleading "Permission denied (publickey)"
	// with no hint the authorized_keys line itself was the problem.
	forced := fmt.Sprintf("%s receive --store %s --base-url %s --slug ssh-artifact",
		binPath, storeDir, baseURL)
	akLine := fmt.Sprintf("restrict,command=%s %s", authorizedKeysQuote(forced), strings.TrimSpace(string(pub)))
	if err := os.WriteFile(authorizedKeys, []byte(akLine+"\n"), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	if err := os.Chmod(hostKey, 0o600); err != nil {
		t.Fatalf("chmod host key: %v", err)
	}

	sshPort := freePort(t)
	pidFile := filepath.Join(dir, "sshd.pid")
	sshdConfig := filepath.Join(dir, "sshd_config")
	cfg := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
AuthorizedKeysFile %s
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
UsePAM no
StrictModes no
PidFile %s
LogLevel ERROR
`, sshPort, hostKey, authorizedKeys, pidFile)
	if err := os.WriteFile(sshdConfig, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write sshd_config: %v", err)
	}

	sshdCmd := exec.Command(sshdPath, "-f", sshdConfig, "-D", "-e")
	var sshdLog syncBuffer
	sshdCmd.Stdout = &sshdLog
	sshdCmd.Stderr = &sshdLog
	if err := sshdCmd.Start(); err != nil {
		t.Skipf("skip: sshd failed to start: %v", err)
	}
	defer func() {
		if sshdCmd.Process != nil {
			_ = sshdCmd.Process.Kill()
			_, _ = sshdCmd.Process.Wait()
		}
	}()

	if err := waitTCP(t, "127.0.0.1:"+strconv.Itoa(sshPort), 5*time.Second); err != nil {
		t.Skipf("skip: unprivileged sshd never accepted connections (environment likely can't run sshd): %v\n--- sshd log ---\n%s", err, sshdLog.String())
	}

	// Pipe an artifact body over `ssh ... < body`. SSH_ORIGINAL_COMMAND is
	// whatever we pass here — the server's forced command wins regardless,
	// per authorized_keys semantics; passing a distinct command proves that.
	body := "published over a real SSH forced command\n"
	sshClient := exec.Command("ssh",
		"-F", "/dev/null",
		"-p", strconv.Itoa(sshPort),
		"-i", clientKey,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		currentUsername()+"@127.0.0.1",
		"this-command-is-ignored-by-the-forced-command",
	)
	sshClient.Stdin = strings.NewReader(body)
	var stdout, stderr bytes.Buffer
	sshClient.Stdout = &stdout
	sshClient.Stderr = &stderr
	if err := sshClient.Run(); err != nil {
		t.Fatalf("ssh receive failed: %v\nstdout=%s\nstderr=%s\n--- sshd log ---\n%s",
			err, stdout.String(), stderr.String(), sshdLog.String())
	}

	got := strings.TrimSpace(stdout.String())
	want := baseURL + "/ssh-artifact"
	if got != want {
		t.Fatalf("ssh receive printed %q, want %q (stderr=%s)", got, want, stderr.String())
	}

	// Now prove the artifact SSH just wrote is actually readable: point a real
	// `demiplane serve` at the same store directory and GET it back.
	verifySrv := startServerWithStoreDir(t, storeDir, serverOpts{})
	res := verifySrv.do(t, "GET", verifySrv.ContentURL+"/ssh-artifact", nil, nil)
	if res.Status != 200 {
		t.Fatalf("GET /ssh-artifact after SSH receive: status=%d", res.Status)
	}
	if string(res.Body) != body {
		t.Fatalf("GET /ssh-artifact body = %q, want %q", res.Body, body)
	}
}

// authorizedKeysQuote wraps s in double quotes per sshd's authorized_keys
// OPTIONS value syntax (sshd(8): "...may span multiple lines...enclosed in
// double quotes... may contain any printable character other than double
// quote and backslash unless escaped by a backslash"). This is sshd's own
// option-parser syntax, NOT POSIX shell quoting — a single-quoted value is
// not recognized as quoted at all and truncates at the first space.
func authorizedKeysQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func currentUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" {
		return u
	}
	out, err := exec.Command("whoami").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return "root"
}
