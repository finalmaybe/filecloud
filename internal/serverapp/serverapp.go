// Package serverapp runs the Filecloud server and its local administration commands.
package serverapp

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
	"strings"
	"sync"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/dav"
	"github.com/mingming-cn/filecloud/internal/health"
	libraryapi "github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/opslog"
	"github.com/mingming-cn/filecloud/internal/storage"
	"github.com/mingming-cn/filecloud/internal/webconsole"
)

const (
	_defaultListen              = "127.0.0.1:8080"
	_defaultGlobalKDFCapacity   = 2
	_defaultSourceIPKDFCapacity = 1
	_defaultUsernameKDFCapacity = 1
	_shutdownPeriod             = 5 * time.Second
	_requestReadPeriod          = 30 * time.Second
)

// BuildInfo identifies one compiled Filecloud executable.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// Run executes one server or local administration command.
func Run(ctx context.Context, program string, args []string, stdin io.Reader, stdout, stderr io.Writer, build BuildInfo) (retErr error) {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s <version|init|serve|gc|integrity|user> [options]", program)
	}
	command := args[0]
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
			return fmt.Errorf("usage: %s version", program)
		}
		return json.NewEncoder(stdout).Encode(build)
	case "init":
		return runInit(ctx, program, args[1:], stderr)
	case "serve":
		return runServe(ctx, program, args[1:], stdout, stderr, build)
	case "gc":
		return runGarbageCollection(ctx, program, args[1:], stdout, stderr)
	case "integrity":
		return runIntegrity(ctx, program, args[1:], stdout, stderr)
	case "user":
		return runUser(ctx, program, args[1:], stdin, stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func runInit(ctx context.Context, program string, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "Filecloud data directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: %s init --data-dir path", program)
	}
	return storage.Init(ctx, *dataDir)
}

func runServe(ctx context.Context, program string, args []string, stdout, stderr io.Writer, build BuildInfo) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "Filecloud data directory")
	listen := flags.String("listen", _defaultListen, "HTTP listen address")
	var rawOrigin *string
	flags.Func("public-origin", "Enable the Web console and WebDAV at this public `origin` (default: both disabled)", func(value string) error {
		rawOrigin = &value
		return nil
	})
	gcAt := flags.String("gc-at", "03:00", "Daily in-process garbage collection time (local HH:MM or off)")
	trashDays := flags.Int("trash-days", 0, "Set saved default recycle bin retention (initially 30 days)")
	historyDays := flags.Int("history-days", 0, "Set saved default snapshot retention (initially 60 days)")
	confirmHistory := flags.String("confirm-history-preview", "", "Confirm the reported impact of shortening the global history window")
	var admins []string
	flags.Func("web-admin-user", "Authorize an existing local account for Web administration (repeatable)", func(value string) error { admins = append(admins, value); return nil })
	globalKDFCapacity := flags.Int("kdf-global-capacity", _defaultGlobalKDFCapacity, "Global concurrent password KDF capacity")
	sourceIPKDFCapacity := flags.Int("kdf-source-ip-capacity", _defaultSourceIPKDFCapacity, "Concurrent password KDF capacity per direct source IP")
	usernameKDFCapacity := flags.Int("kdf-username-capacity", _defaultUsernameKDFCapacity, "Concurrent password KDF capacity per canonical username")
	uploadDefaults := storage.DefaultUploadConfig()
	uploadGlobalCapacity := flags.Int("upload-global-capacity", uploadDefaults.GlobalConcurrency, "Global concurrent object PUT capacity")
	uploadUserCapacity := flags.Int("upload-user-capacity", uploadDefaults.UserConcurrency, "Concurrent object PUT capacity per user")
	uploadReadTimeout := flags.Duration("upload-read-timeout", uploadDefaults.RequestTimeout, "Maximum time to read one object PUT")
	uploadBudgetBytes := flags.Int64("upload-budget-bytes", uploadDefaults.BudgetBytes, "Object bytes accepted per user upload window")
	uploadBudgetWindow := flags.Duration("upload-budget-window", uploadDefaults.BudgetWindow, "Rolling object upload accounting window")
	headDefaults := libraryapi.DefaultHeadValidationConfig()
	headGlobalCapacity := flags.Int("head-global-capacity", headDefaults.GlobalConcurrency, "Global concurrent Head validation capacity")
	headValidationTimeout := flags.Duration("head-validation-timeout", headDefaults.RequestTimeout, "Maximum time for one Head validation")
	headMaxSnapshotDepth := flags.Int("head-max-snapshot-depth", headDefaults.MaxSnapshotDepth, "Maximum Head snapshot tree depth")
	headMaxTraversalContexts := flags.Int("head-max-traversal-contexts", headDefaults.MaxTraversalContexts, "Maximum Head graph traversal contexts")
	headMaxParentDepth := flags.Int("head-max-parent-depth", headDefaults.MaxCommitDepth, "Maximum second-parent traversal depth")
	headMaxIntroducedCommits := flags.Int("head-max-introduced-commits", headDefaults.MaxIntroducedCommits, "Maximum commits introduced by a Head update")
	headMaxValidatedObjects := flags.Int("head-max-validated-objects", headDefaults.MaxValidatedObjects, "Maximum deduplicated objects validated by a Head update")
	historyDefaults := libraryapi.DefaultHistoryConfig()
	historyGlobalCapacity := flags.Int("history-global-capacity", historyDefaults.GlobalConcurrency, "Global concurrent history traversal capacity")
	historyUserCapacity := flags.Int("history-user-capacity", historyDefaults.UserConcurrency, "Concurrent history traversal capacity per user")
	historyReadTimeout := flags.Duration("history-read-timeout", historyDefaults.RequestTimeout, "Maximum time for one history traversal")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: %s serve --data-dir path [--listen address] [--public-origin origin] [--kdf-global-capacity n] [--kdf-source-ip-capacity n] [--kdf-username-capacity n]", program)
	}
	if *globalKDFCapacity < 1 || *sourceIPKDFCapacity < 1 || *usernameKDFCapacity < 1 {
		return errors.New("kdf concurrency limits must be positive")
	}
	uploadConfig := storage.UploadConfig{
		GlobalConcurrency: *uploadGlobalCapacity,
		UserConcurrency:   *uploadUserCapacity,
		RequestTimeout:    *uploadReadTimeout,
		BudgetBytes:       *uploadBudgetBytes,
		BudgetWindow:      *uploadBudgetWindow,
	}
	if uploadConfig.GlobalConcurrency < 1 || uploadConfig.UserConcurrency < 1 || uploadConfig.RequestTimeout <= 0 || uploadConfig.BudgetBytes < 1 || uploadConfig.BudgetWindow <= 0 {
		return errors.New("upload limits must be positive")
	}
	headConfig := libraryapi.HeadValidationConfig{
		GlobalConcurrency:    *headGlobalCapacity,
		RequestTimeout:       *headValidationTimeout,
		MaxSnapshotDepth:     *headMaxSnapshotDepth,
		MaxTraversalContexts: *headMaxTraversalContexts,
		MaxCommitDepth:       *headMaxParentDepth,
		MaxIntroducedCommits: *headMaxIntroducedCommits,
		MaxValidatedObjects:  *headMaxValidatedObjects,
	}
	if headConfig.GlobalConcurrency < 1 || headConfig.RequestTimeout <= 0 || headConfig.MaxSnapshotDepth < 1 ||
		headConfig.MaxTraversalContexts < 1 || headConfig.MaxCommitDepth < 1 || headConfig.MaxIntroducedCommits < 1 || headConfig.MaxValidatedObjects < 1 {
		return errors.New("head validation limits must be positive")
	}
	historyConfig := libraryapi.HistoryConfig{
		GlobalConcurrency: *historyGlobalCapacity,
		UserConcurrency:   *historyUserCapacity,
		RequestTimeout:    *historyReadTimeout,
	}
	if historyConfig.GlobalConcurrency < 1 || historyConfig.UserConcurrency < 1 || historyConfig.RequestTimeout <= 0 {
		return errors.New("history limits must be positive")
	}
	if len(admins) > 0 && rawOrigin == nil {
		return errors.New("--web-admin-user requires --public-origin")
	}
	if *trashDays < 0 || *historyDays < 0 || *trashDays > 36500 || *historyDays > 36500 {
		return errors.New("retention must be 1–36500 days")
	}
	schedule, err := parseGCSchedule(*gcAt)
	if err != nil {
		return err
	}
	var origin *auth.BrowserOrigin
	if rawOrigin != nil {
		origin, err = auth.NewBrowserOrigin(*rawOrigin, *listen)
		if err != nil {
			return fmt.Errorf("--public-origin: %w", err)
		}
	}
	return serve(ctx, schedule, *confirmHistory, storage.Retention{TrashDays: *trashDays, HistoryDays: *historyDays}, *dataDir, *listen, stdout, stderr, origin, webconsole.Config{AdminUsers: admins, Build: webconsole.Build(build)}, auth.HandlerConfig{
		GlobalKDFLimit:   *globalKDFCapacity,
		SourceIPKDFLimit: *sourceIPKDFCapacity,
		UsernameKDFLimit: *usernameKDFCapacity,
	}, uploadConfig, headConfig, historyConfig)
}

func runGarbageCollection(ctx context.Context, program string, args []string, stdout, stderr io.Writer) (retErr error) {
	flags := flag.NewFlagSet("gc", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "Filecloud data directory")
	dryRun := flags.Bool("dry-run", false, "Report candidates without deleting objects")
	gracePeriod := flags.Duration("grace-period", storage.MinimumGarbageCollectionGracePeriod, "Minimum age of unpublished objects eligible for deletion")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: %s gc --data-dir path [--dry-run] [--grace-period duration]", program)
	}
	if *gracePeriod < storage.MinimumGarbageCollectionGracePeriod {
		return fmt.Errorf("gc grace period must be at least %s", storage.MinimumGarbageCollectionGracePeriod)
	}
	collector, err := storage.OpenGarbageCollector(ctx, *dataDir)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, collector.Close()) }()
	report, err := collector.Collect(ctx, storage.GarbageCollectionOptions{
		DryRun: *dryRun, GracePeriod: *gracePeriod, Now: time.Now(),
	})
	if err != nil {
		return err
	}
	for _, stats := range report.Objects {
		if _, err := fmt.Fprintf(stdout, "%s objects=%d bytes=%d\n", stats.Type, stats.Count, stats.Bytes); err != nil {
			return fmt.Errorf("write gc report: %w", err)
		}
	}
	return nil
}

func runIntegrity(ctx context.Context, program string, args []string, stdout, stderr io.Writer) (retErr error) {
	if len(args) == 0 || args[0] != "check" {
		return fmt.Errorf("usage: %s integrity check --data-dir path", program)
	}
	flags := flag.NewFlagSet("integrity check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "Filecloud data directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *dataDir == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: %s integrity check --data-dir path", program)
	}
	checker, err := storage.OpenIntegrityChecker(ctx, *dataDir)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, checker.Close()) }()
	report, err := checker.Check(ctx)
	if err != nil {
		return err
	}
	for _, issue := range report.Issues {
		if _, err := fmt.Fprintf(stdout, "library=%s owner=%s object=%s id=%s state=%s\n",
			issue.LibraryID, issue.OwnerUserID, issue.ObjectType, issue.ObjectID, issue.State); err != nil {
			return fmt.Errorf("write integrity issue: %w", err)
		}
	}
	if _, err := fmt.Fprintf(stdout, "integrity libraries=%d objects=%d issues=%d\n",
		report.Libraries, report.Objects, len(report.Issues)); err != nil {
		return fmt.Errorf("write integrity summary: %w", err)
	}
	if len(report.Issues) != 0 {
		return fmt.Errorf("integrity check found %d issues", len(report.Issues))
	}
	return nil
}

func runUser(ctx context.Context, program string, args []string, stdin io.Reader, stdout, stderr io.Writer) (retErr error) {
	if len(args) == 0 || (args[0] != "add" && args[0] != "reset-password") {
		return fmt.Errorf("usage: %s user <add|reset-password> --data-dir path --username name --password-stdin", program)
	}
	operation := args[0]
	flags := flag.NewFlagSet("user "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "Filecloud data directory")
	username := flags.String("username", "", "Username")
	passwordStdin := flags.Bool("password-stdin", false, "Read password from standard input")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *dataDir == "" || *username == "" || !*passwordStdin || flags.NArg() != 0 {
		return fmt.Errorf("usage: %s user %s --data-dir path --username name --password-stdin", program, operation)
	}
	display, err := auth.ValidateUsername(*username)
	if err != nil {
		return err
	}
	password, err := readPassword(stdin)
	if err != nil {
		return err
	}
	defer clear(password)
	hash, err := auth.HashPassword(password, auth.DefaultParams(), nil)
	if err != nil {
		return err
	}
	store, err := storage.OpenForAdmin(ctx, *dataDir)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, store.Close()) }()
	now := time.Now().UTC()
	if operation == "add" {
		id, err := auth.NewUserID(nil)
		if err != nil {
			return err
		}
		if err := store.CreateUser(ctx, storage.User{ID: id, Username: display, PasswordHash: hash}, now); err != nil {
			return err
		}
	} else if err := store.ResetPassword(ctx, display, hash, now); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "user %s: %s\n", operation, display)
	return err
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

type requestTracker struct {
	handler http.Handler
	mu      sync.Mutex
	stopped bool
	active  int
	allDone chan struct{}
}

func newRequestTracker(handler http.Handler) *requestTracker {
	return &requestTracker{handler: handler, allDone: make(chan struct{})}
}

func (t *requestTracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	t.active++
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.active--
		if t.stopped && t.active == 0 {
			close(t.allDone)
		}
		t.mu.Unlock()
	}()
	t.handler.ServeHTTP(w, r)
}

func (t *requestTracker) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.stopped = true
	if t.active == 0 {
		close(t.allDone)
	}
}

func (t *requestTracker) wait() {
	<-t.allDone
}

func serve(ctx context.Context, schedule gcSchedule, confirmHistory string, defaults storage.Retention, dataDir, address string, stdout, stderr io.Writer, origin *auth.BrowserOrigin, webConfig webconsole.Config, authConfig auth.HandlerConfig, uploadConfig storage.UploadConfig, headConfig libraryapi.HeadValidationConfig, historyConfig libraryapi.HistoryConfig) (retErr error) {
	store, err := storage.OpenForServe(ctx, dataDir)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, store.Close()) }()

	previous, err := store.Defaults(ctx)
	if err != nil {
		return err
	}
	if defaults.TrashDays == 0 {
		defaults.TrashDays = previous.TrashDays
	}
	if defaults.HistoryDays == 0 {
		defaults.HistoryDays = previous.HistoryDays
	}
	if defaults.HistoryDays < previous.HistoryDays {
		p, err := store.PreviewDefaultHistory(ctx, defaults.HistoryDays, time.Now())
		if err != nil {
			return err
		}
		if p.Token != "" && p.Token != confirmHistory {
			return fmt.Errorf("global history preview: %d expired snapshots (%s to %s UTC), %d restored records, %d reclaimable block bytes; rerun with --confirm-history-preview %s", p.Count, p.First, p.Last, p.RestoredRecords, p.Bytes, p.Token)
		}
	}
	if defaults != previous {
		if err := store.SetDefaults(ctx, defaults, time.Now()); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	logger := log.New(stderr, "", 0)
	sessions, err := auth.NewHandler(store, logger, authConfig)
	if err != nil {
		return errors.Join(err, listener.Close())
	}
	libraries, err := libraryapi.NewHandler(store, logger, libraryapi.Config{BrowserOrigin: origin, Upload: uploadConfig, HeadValidation: headConfig, History: historyConfig})
	if err != nil {
		return errors.Join(err, listener.Close())
	}
	mux := http.NewServeMux()
	healthHandler := health.NewHandler(store, logger)
	mux.Handle("/healthz", healthHandler)
	mux.Handle("/readyz", healthHandler)
	// Keep unmatched machine-protocol paths out of the browser HTML handler.
	mux.Handle("/v1", healthHandler)
	mux.Handle("/v1/", healthHandler)
	mux.Handle("/dav", healthHandler)
	mux.Handle("/dav/", healthHandler)
	mux.Handle("/v1/sessions", sessions)
	mux.Handle("/v1/sessions/current", sessions)
	mux.Handle("/v1/libraries", libraries)
	mux.Handle("/v1/libraries/", libraries)
	var root http.Handler = mux
	if origin != nil {
		webConfig.Sessions, webConfig.Store, webConfig.Catalog = sessions, store, libraries
		web, err := webconsole.NewHandler(origin, webConfig)
		if err != nil {
			return errors.Join(err, listener.Close())
		}
		mux.Handle("/", web)
		handler := &dav.Handler{Origin: origin, Store: store, Catalog: libraries}
		root = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/dav" || strings.HasPrefix(r.URL.Path, "/dav/") {
				handler.ServeHTTP(w, r)
				return
			}
			mux.ServeHTTP(w, r)
		})
	} else {
		mux.Handle("/", healthHandler)
	}
	requests := newRequestTracker(root)
	server := &http.Server{
		Handler: requests, ErrorLog: log.New(opslog.RedactedWriter(logger, "serve", "", "http_server"), "", 0),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       _requestReadPeriod,
	}
	if _, err := fmt.Fprintf(stdout, "listening on %s\n", listener.Addr()); err != nil {
		return errors.Join(fmt.Errorf("write listen address: %w", err), listener.Close())
	}

	stopGC := startOnlineGC(ctx, schedule, store, logger)
	defer stopGC()
	shutdownErr := make(chan error, 1)
	stopShutdown := context.AfterFunc(ctx, func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				shutdownErr <- fmt.Errorf("shutdown panic: %v", recovered)
			}
		}()
		requests.stop()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), _shutdownPeriod)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			opslog.Error(logger, "serve", "", "force_shutdown", err)
			closeErr := server.Close()
			requests.wait()
			shutdownErr <- errors.Join(err, closeErr)
			return
		}
		requests.wait()
		shutdownErr <- nil
	})
	serveErr := server.Serve(listener)
	if stopShutdown() {
		return fmt.Errorf("serve http: %w", serveErr)
	}
	if err := <-shutdownErr; err != nil {
		return fmt.Errorf("shutdown http: %w", err)
	}
	if !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", serveErr)
	}
	return nil
}
