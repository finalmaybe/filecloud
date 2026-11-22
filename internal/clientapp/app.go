// Package clientapp runs the Filecloud client and transitional server-command compatibility entry.
package clientapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/opslog"
	"github.com/mingming-cn/filecloud/internal/serverapp"
)

const _clientRequestPeriod = 3 * time.Minute

// BuildInfo identifies one compiled Filecloud client executable.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// Run executes one client command or a transitional server compatibility command.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, build BuildInfo) (retErr error) {
	if len(args) > 0 && args[0] == "--machine" {
		return runMachine(ctx, args[1:], stdin, stdout, build)
	}
	if len(args) == 0 {
		return errors.New("usage: filecloud-client <version|init|serve|gc|integrity|user|login|logout|library> [options]")
	}
	command := args[0]
	switch command {
	case "version", "login", "logout", "library":
	default:
		return serverapp.Run(ctx, "filecloud-client", args, stdin, stdout, stderr, serverBuildInfo(build))
	}
	logger := log.New(stderr, "", 0)
	opslog.Info(logger, command, "", "start")
	defer func() {
		if retErr != nil {
			opslog.Error(logger, command, "", "complete", retErr)
			return
		}
		opslog.Info(logger, command, "", "complete")
	}()

	switch command {
	case "version":
		if len(args) != 1 {
			return errors.New("usage: filecloud-client version")
		}
		return json.NewEncoder(stdout).Encode(struct {
			Version   string
			Commit    string
			BuildDate string
		}{Version: build.Version, Commit: build.Commit, BuildDate: build.BuildDate})
	case "login":
		return runLogin(ctx, args[1:], stdin, stdout, stderr)
	case "logout":
		return runLogout(ctx, args[1:], stdin, stderr)
	case "library":
		return runLibrary(ctx, args[1:], stdin, stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return Run(ctx, args, stdin, stdout, stderr, BuildInfo{
		Version: "dev", Commit: "unknown", BuildDate: "unknown",
	})
}

func serverBuildInfo(build BuildInfo) serverapp.BuildInfo {
	return serverapp.BuildInfo{Version: build.Version, Commit: build.Commit, BuildDate: build.BuildDate}
}

func runLogin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "Filecloud server URL")
	username := flags.String("username", "", "Username")
	deviceName := flags.String("device-name", "", "Device name")
	passwordStdin := flags.Bool("password-stdin", false, "Read password from standard input")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *server == "" || *username == "" || *deviceName == "" || !*passwordStdin || flags.NArg() != 0 {
		return errors.New("usage: filecloud-client login --server url --username name --device-name name --password-stdin")
	}
	base, err := validateServerURL(*server)
	if err != nil {
		return err
	}
	password, err := readPassword(stdin)
	if err != nil {
		return err
	}
	defer clear(password)
	body, err := json.Marshal(struct {
		Username   string
		Password   string
		DeviceName string
	}{Username: *username, Password: string(password), DeviceName: *deviceName})
	if err != nil {
		return fmt.Errorf("encode login request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.JoinPath("v1/sessions").String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create login request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := noRedirectClient().Do(request)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	closeErr := response.Body.Close()
	if readErr != nil {
		return errors.Join(fmt.Errorf("read login response: %w", readErr), closeResponseError("login", closeErr))
	}
	if closeErr != nil {
		return closeResponseError("login", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		return remoteResponseError(fmt.Sprintf("login failed: server returned %s", response.Status), response.StatusCode)
	}
	if _, err := stdout.Write(responseBody); err != nil {
		return fmt.Errorf("write login response: %w", err)
	}
	return nil
}

func runLogout(ctx context.Context, args []string, stdin io.Reader, stderr io.Writer) error {
	flags := flag.NewFlagSet("logout", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "Filecloud server URL")
	tokenStdin := flags.Bool("token-stdin", false, "Read token from standard input")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *server == "" || !*tokenStdin || flags.NArg() != 0 {
		return errors.New("usage: filecloud-client logout --server url --token-stdin")
	}
	base, err := validateServerURL(*server)
	if err != nil {
		return err
	}
	token, err := readLineSecret(stdin, 4096, "token")
	if err != nil {
		return err
	}
	defer clear(token)
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, base.JoinPath("v1/sessions/current").String(), nil)
	if err != nil {
		return fmt.Errorf("create logout request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	response, err := noRedirectClient().Do(request)
	if err != nil {
		return fmt.Errorf("logout request: %w", err)
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	closeErr := response.Body.Close()
	if readErr != nil {
		return errors.Join(fmt.Errorf("read logout response: %w", readErr), closeResponseError("logout", closeErr))
	}
	if closeErr != nil {
		return closeResponseError("logout", closeErr)
	}
	if response.StatusCode != http.StatusNoContent {
		return remoteResponseError(fmt.Sprintf("logout failed: server returned %s", response.Status), response.StatusCode)
	}
	return nil
}

func closeResponseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close %s response: %w", operation, err)
}

func readPassword(reader io.Reader) ([]byte, error) {
	password, err := readLineSecret(reader, 1024, "password")
	if err != nil {
		return nil, err
	}
	if err := auth.ValidatePassword(password); err != nil {
		return nil, err
	}
	return password, nil
}

func readLineSecret(reader io.Reader, maximum int, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(maximum+3)))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	data = bytes.TrimSuffix(data, []byte("\r\n"))
	data = bytes.TrimSuffix(data, []byte("\n"))
	if len(data) > maximum {
		return nil, fmt.Errorf("%s is too long", name)
	}
	if bytes.ContainsAny(data, "\r\n") {
		return nil, fmt.Errorf("%s must be one line", name)
	}
	return data, nil
}

func validateServerURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("server must be an absolute url without credentials, path, query, or fragment")
	}
	if parsed.Scheme != "https" {
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if parsed.Scheme != "http" || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, errors.New("server must use https unless it is a loopback url")
		}
	}
	return parsed, nil
}

func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout: _clientRequestPeriod,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
