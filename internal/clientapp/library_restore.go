package clientapp

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/mingming-cn/filecloud/internal/object"
)

func runLibraryRestore(ctx context.Context, args []string, stdout, stderr io.Writer, config libraryClientConfig) error {
	flags := newFlagSet("library restore", stderr)
	clientDir := flags.String("client-dir", "", "Filecloud client state directory")
	worktree := flags.String("worktree", "", "Bound worktree directory")
	commitID := flags.String("commit", "", "Complete historical CommitId")
	sourcePath := flags.String("path", "", "Protocol-relative snapshot path or .")
	confirm := flags.String("confirm", "", "Confirm a pending restore candidate")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *clientDir == "" || *worktree == "" || flags.NArg() != 0 {
		return errors.New("usage: filecloud-client library restore --client-dir path --worktree path --commit 64-hex --path relative-path-or-dot | --confirm 12-hex-prefix")
	}
	var commitSet, pathSet, confirmSet bool
	flags.Visit(func(value *flag.Flag) {
		switch value.Name {
		case "commit":
			commitSet = true
		case "path":
			pathSet = true
		case "confirm":
			confirmSet = true
		}
	})
	if confirmSet {
		if commitSet || pathSet {
			return errors.New("restore --confirm cannot combine with --commit or --path")
		}
		if len(*confirm) != deleteCandidatePrefixLen || !isLowerHex(*confirm) {
			return errors.New("restore --confirm must be a 12-character lowercase hexadecimal prefix")
		}
		return runRestorePublication(ctx, *clientDir, *worktree, _confirmRestorePublicationRequest{prefix: *confirm, warning: stderr}, stdout, stderr, config)
	}
	if !commitSet {
		return errors.New("restore preview requires --commit")
	}
	if !object.ValidID(*commitID) {
		return errors.New("restore --commit must be a complete 64-character lowercase object ID")
	}
	if !pathSet || !object.ValidPath(*sourcePath) {
		return errors.New("restore preview requires a canonical --path")
	}
	return runRestorePublication(ctx, *clientDir, *worktree, _previewRestorePublicationRequest{sourceCommit: *commitID, sourcePath: *sourcePath, warning: stderr}, stdout, stderr, config)
}

func runRestorePublication(ctx context.Context, clientDir, worktree string, request _publicationRequester,
	stdout, stderr io.Writer, config libraryClientConfig) (retErr error) {
	session, err := openLockedBoundWorktreeSession(ctx, clientDir, worktree, config)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, session.Close()) }()
	var lifecycle _publicationLifecycle
	outcome, err := lifecycle.Advance(ctx, session, request)
	if err != nil {
		return err
	}
	switch outcome.Kind() {
	case _publicationOutcomePreviewReady:
		return writeRestorePreview(stdout, clientDir, worktree, *outcome.restorePreview)
	case _publicationOutcomeRestoreNoOp:
		return writeRestoreNoOp(stdout, outcome.restorePreview.sourceCommit, outcome.restorePreview.sourcePath)
	case _publicationOutcomeCheckoutPending:
		options := session.bindOptions()
		options.scanConfig, err = session.loadScanConfig(ctx, stderr)
		if err != nil {
			return err
		}
		return continueSyncCheckout(ctx, session.db, options, session.binding, *outcome.checkout, stdout, session.config)
	default:
		return _errPublicationRequestInvalid
	}
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func writeRestoreNoOp(output io.Writer, source, path string) error {
	_, err := fmt.Fprintf(output, "restore no-op: source commit=%s path=%s already matches current state\n", source, path)
	if err != nil {
		return fmt.Errorf("write restore no-op: %w", err)
	}
	return nil
}

func writeRestorePreview(output io.Writer, clientDir, worktree string, preview restorePreview) error {
	if _, err := fmt.Fprintf(output, "source commit: %s\nsource path: %s\nexpected head: %s\ncandidate: %s\ncreated paths: %d\nupdated paths: %d\ntype replacements: %d\nremoved descendants by type replacement: %d\npreserved current-only paths: %d\nchanged paths:\n", preview.sourceCommit, preview.sourcePath, preview.expectedHead, preview.confirmationPrefix(), preview.createdCount, preview.updatedCount, preview.typeReplacementCount, preview.removedDescendantCount, preview.preservedCurrentOnlyCount); err != nil {
		return fmt.Errorf("write restore preview: %w", err)
	}
	for _, path := range preview.changedPaths {
		if _, err := fmt.Fprintf(output, "%s\n", path); err != nil {
			return fmt.Errorf("write restore preview path: %w", err)
		}
	}
	if _, err := fmt.Fprintf(output, "truncated: %t\nconfirm: filecloud-client library restore --client-dir %s --worktree %s --confirm %s\n", preview.truncated, clientDir, worktree, preview.confirmationPrefix()); err != nil {
		return fmt.Errorf("write restore confirmation: %w", err)
	}
	return nil
}
