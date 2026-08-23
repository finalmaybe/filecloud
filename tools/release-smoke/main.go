// Command release-smoke verifies packaged Filecloud client and server binaries through their public interfaces.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	_libraryID = "01234567-89ab-4def-8123-456789abcdef"
	_deviceID  = "12345678-9abc-4def-8123-456789abcdef"
)

type versionInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

type loginEnvelope struct {
	Session struct {
		AccessToken string
	}
}

type headEnvelope struct {
	Head struct {
		CommitID *string `json:"CommitId"`
	}
}

func main() {
	clientBinary := flag.String("client-binary", "", "path to the Filecloud client binary")
	serverBinary := flag.String("server-binary", "", "path to the Filecloud server binary")
	version := flag.String("version", "", "expected release version")
	flag.Parse()
	if *clientBinary == "" || *serverBinary == "" || *version == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/release-smoke --client-binary path --server-binary path --version version")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := smoke(ctx, *clientBinary, *serverBinary, *version); err != nil {
		fmt.Fprintln(os.Stderr, "release smoke:", err)
		os.Exit(1)
	}
	fmt.Println("release smoke passed")
}

func smoke(ctx context.Context, clientBinary, serverBinary, expectedVersion string) error {
	absoluteClient, err := filepath.Abs(clientBinary)
	if err != nil {
		return fmt.Errorf("resolve client binary: %w", err)
	}
	absoluteServer, err := filepath.Abs(serverBinary)
	if err != nil {
		return fmt.Errorf("resolve server binary: %w", err)
	}
	if err := verifyVersion(ctx, "client", absoluteClient, expectedVersion); err != nil {
		return err
	}
	if err := verifyVersion(ctx, "server", absoluteServer, expectedVersion); err != nil {
		return err
	}

	temporaryRoot, err := os.MkdirTemp(".", ".filecloud-release-smoke-")
	if err != nil {
		return fmt.Errorf("create smoke root: %w", err)
	}
	defer os.RemoveAll(temporaryRoot)
	root, err := filepath.Abs(temporaryRoot)
	if err != nil {
		return fmt.Errorf("resolve smoke root: %w", err)
	}
	dataDir := filepath.Join(root, "data")
	clientDir := filepath.Join(root, "client")
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}
	if _, err := runBinary(ctx, absoluteServer, nil, "init", "--data-dir", dataDir); err != nil {
		return err
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	defer clear(password)
	passwordInput := append(bytes.Clone(password), '\n')
	defer clear(passwordInput)
	if _, err := runBinary(ctx, absoluteServer, passwordInput, "user", "add", "--data-dir", dataDir, "--username", "release-smoke", "--password-stdin"); err != nil {
		return err
	}

	server, err := startServer(ctx, absoluteServer, dataDir)
	if err != nil {
		return err
	}
	defer server.stop()

	loginOutput, err := runBinary(ctx, absoluteClient, passwordInput, "login", "--server", server.url, "--username", "release-smoke", "--device-name", "release-smoke", "--password-stdin")
	if err != nil {
		return err
	}
	var login loginEnvelope
	if err := json.Unmarshal(loginOutput, &login); err != nil || login.Session.AccessToken == "" {
		return fmt.Errorf("decode login response: token_present=%t: %w", login.Session.AccessToken != "", err)
	}
	tokenInput := []byte(login.Session.AccessToken + "\n")
	defer clear(tokenInput)
	if _, err := runBinary(ctx, absoluteClient, tokenInput, "library", "create", "--server", server.url,
		"--library-id", _libraryID, "--name", "Release Smoke", "--token-stdin"); err != nil {
		return err
	}
	inspectOutput, err := runBinary(ctx, absoluteClient, tokenInput, "library", "inspect", "--server", server.url,
		"--library-id", _libraryID, "--token-stdin")
	if err != nil {
		return err
	}
	if !bytes.Contains(inspectOutput, []byte(_libraryID)) {
		return errors.New("inspect response has no created library")
	}
	listOutput, err := runBinary(ctx, absoluteClient, tokenInput, "library", "list", "--server", server.url, "--token-stdin")
	if err != nil {
		return err
	}
	if !bytes.Contains(listOutput, []byte(_libraryID)) {
		return errors.New("list response has no created library")
	}
	if _, err := runBinary(ctx, absoluteClient, tokenInput, "library", "bind", "--client-dir", clientDir,
		"--server", server.url, "--library-id", _libraryID, "--worktree", worktree, "--device-id", _deviceID, "--token-stdin"); err != nil {
		return err
	}
	initialHead, err := getHead(ctx, server.url, login.Session.AccessToken)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(worktree, "smoke.txt"), []byte("filecloud release smoke\n"), 0o644); err != nil {
		return fmt.Errorf("write smoke file: %w", err)
	}
	if _, err := runBinary(ctx, absoluteClient, nil, "library", "sync", "--client-dir", clientDir, "--worktree", worktree); err != nil {
		return err
	}
	updatedHead, err := getHead(ctx, server.url, login.Session.AccessToken)
	if err != nil {
		return err
	}
	if initialHead == "" || updatedHead == "" || initialHead == updatedHead {
		return fmt.Errorf("sync did not advance Head: initial=%q updated=%q", initialHead, updatedHead)
	}
	return nil
}

func verifyVersion(ctx context.Context, name, binary, expected string) error {
	output, err := runBinary(ctx, binary, nil, "version")
	if err != nil {
		return err
	}
	return validateVersionOutput(name, output, expected)
}

func validateVersionOutput(name string, output []byte, expected string) error {
	var version versionInfo
	if err := json.Unmarshal(output, &version); err != nil {
		return fmt.Errorf("decode %s version output: %w", name, err)
	}
	if version.Version != expected || version.Commit == "" || version.Commit == "unknown" || version.BuildDate == "" || version.BuildDate == "unknown" {
		return fmt.Errorf("unexpected %s version metadata: %+v", name, version)
	}
	return nil
}

func randomPassword() ([]byte, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("generate smoke password: %w", err)
	}
	password := make([]byte, base64.RawURLEncoding.EncodedLen(len(random)))
	base64.RawURLEncoding.Encode(password, random)
	clear(random)
	return password, nil
}

type runningServer struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	url    string
	stderr *bytes.Buffer
}

func startServer(parent context.Context, binary, dataDir string) (*runningServer, error) {
	ctx, cancel := context.WithCancel(parent)
	command := exec.CommandContext(ctx, binary, "serve", "--data-dir", dataDir, "--listen", "127.0.0.1:0")
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open serve output: %w", err)
	}
	stderr := new(bytes.Buffer)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start serve: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		readErr := fmt.Errorf("read serve address: %w: %s", scanner.Err(), stderr.String())
		return nil, errors.Join(readErr, cancelAndWait(cancel, command))
	}
	address, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "listening on ")
	if !ok || !strings.HasPrefix(address, "127.0.0.1:") {
		addressErr := fmt.Errorf("unexpected serve address %q", scanner.Text())
		return nil, errors.Join(addressErr, cancelAndWait(cancel, command))
	}
	return &runningServer{cancel: cancel, cmd: command, url: "http://" + address, stderr: stderr}, nil
}

func (s *runningServer) stop() {
	if err := cancelAndWait(s.cancel, s.cmd); err != nil {
		fmt.Fprintln(os.Stderr, "release smoke: stop serve:", err, s.stderr.String())
	}
}

func cancelAndWait(cancel context.CancelFunc, command *exec.Cmd) error {
	cancel()
	err := command.Wait()
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		return nil
	}
	if err != nil {
		return fmt.Errorf("wait for stopped process: %w", err)
	}
	return nil
}

func runBinary(ctx context.Context, binary string, stdin []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("run filecloud %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func getHead(ctx context.Context, server, token string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/v1/libraries/"+_libraryID+"/head", nil)
	if err != nil {
		return "", fmt.Errorf("create Head request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("get Head: %w", err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if readErr != nil {
		return "", fmt.Errorf("read Head response: %w", readErr)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get Head returned %s: %s", response.Status, body)
	}
	var envelope headEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("decode Head response: %w", err)
	}
	if envelope.Head.CommitID == nil {
		return "", errors.New("Head response has no CommitId")
	}
	return *envelope.Head.CommitID, nil
}
