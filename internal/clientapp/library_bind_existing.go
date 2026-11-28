package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"

	"github.com/mingming-cn/filecloud/internal/object"
	"golang.org/x/text/cases"
)

type folderChanges struct {
	Count int
	Paths []string
}

func (c *folderChanges) add(name string) {
	c.Count++
	if len(c.Paths) < 50 {
		c.Paths = append(c.Paths, name)
	}
}

type folderMergePreview struct {
	Candidate                  string
	Identical                  bool
	Upload, Download, Conflict folderChanges
}

// Binding to a verified empty snapshot makes both trees additions in the existing
// sync engine. A missing path on either side can never mean a deletion here.
func bindExistingFolder(ctx context.Context, options bindOptions, owner string, head remoteHead, snapshot worktreeSnapshot, emptyRoot string, out io.Writer, config libraryClientConfig) (retErr error) {
	if head.CommitID == nil {
		return staleSyncCandidate("library has no published version; check the folder again")
	}
	headID := *head.CommitID
	identity := options.serverURL + "\x00" + owner + "\x00" + options.libraryID + "\x00" + options.worktree + "\x00" + headID + snapshot.root
	if rules := ignoreRulesID(options.scanConfig.ignore); rules != "" {
		identity += "\x00" + rules
	}
	candidate := object.ID([]byte(identity))
	if !options.previewMerge && options.mergeCandidate != candidate {
		return staleSyncCandidate("folder or library changed; preview the merge again")
	}
	commit, err := getRemoteCommit(ctx, options.base, options.libraryID, options.token, headID)
	if err != nil {
		return err
	}
	if commit.AuthorUserID != owner {
		return errors.New("library Head has a different owner")
	}
	preview := folderMergePreview{Candidate: candidate, Identical: snapshot.root == commit.Root}
	baseID, paths := headID, snapshot.paths
	var remote []checkoutPath
	if !preview.Identical {
		remote, err = _verifiedRemotePaths(ctx, options, commit.Root, _newReplayBudget())
		if err != nil {
			return err
		}
		if options.scanConfig.ignore.Active() {
			var omitted []checkoutPath
			remote = slices.DeleteFunc(remote, func(path checkoutPath) bool {
				if ignoreSyncPath(options.scanConfig.ignore, path.path) {
					omitted = append(omitted, path)
					return true
				}
				return false
			})
			if err := validateIgnoreCheckoutTargets(snapshot.ignored, remote); err != nil {
				return err
			}
			// Ignored remote entries may make the complete roots differ even when
			// every participating path is already present in the existing folder.
			local := make(map[string]checkoutPath, len(snapshot.paths))
			for _, path := range snapshot.paths {
				local[path.path] = path
			}
			preview.Identical = len(local) == len(remote)
			for _, path := range remote {
				value := local[path.path]
				preview.Identical = preview.Identical && value.kind == path.kind && (path.kind == "Directory" || value.id == path.id && value.mtime == path.mtime)
			}
			if preview.Identical {
				paths = append(paths, omittedForIndex(omitted, options.scanConfig)...)
			}
		}
	}
	if !preview.Identical {
		if err := preview.compare(snapshot.paths, remote); err != nil {
			return err
		}
		// Find a real published empty mainline version; never invent a common ancestor.
		budget := _newReplayBudget()
		for commit.Root != emptyRoot {
			if len(commit.Parents) == 0 || budget.commitFetches >= budget.commitLimit {
				return errMergeBaseUnavailable
			}
			baseID = commit.Parents[0]
			commit, err = _budgetedRemoteCommit(ctx, options, baseID, budget)
			if err != nil {
				return err
			}
			if commit.AuthorUserID != owner {
				return errors.New("library history has a different owner")
			}
		}
		paths = nil
	}
	if options.previewMerge {
		return json.NewEncoder(out).Encode(preview)
	}
	if _, err := rescanRoot(options, snapshot.root); err != nil {
		return staleSyncCandidate("folder changed; preview the merge again")
	}
	latest, err := getRemoteHead(ctx, options.base, options.libraryID, options.token)
	if err != nil {
		return err
	}
	if latest.CommitID == nil || *latest.CommitID != headID {
		return staleSyncCandidate("library changed; preview the merge again")
	}
	db, err := initializeClientDB(ctx, options.clientDir, config.syncDirectory)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, db.Close()) }()
	binding := clientBinding{ServerURL: options.serverURL, LibraryID: options.libraryID, Worktree: options.worktree, UserID: owner, DeviceID: options.deviceID, SyncBase: baseID, SyncBaseRoot: commit.Root, HeadETag: latest.ETag}
	if err := finalizeBinding(ctx, db, binding, options.token, paths); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "library bound; run library sync to synchronize the folder")
	return err
}

var errMergeBaseUnavailable = errors.New("no verified empty mainline snapshot within sync history budget; sync to a new empty folder instead")
var errMergeNameCollision = errors.New("local and remote names differ only in case; rename one before syncing")

func (p *folderMergePreview) compare(local, remote []checkoutPath) error {
	type pair struct{ local, remote checkoutPath }
	entries := map[string]pair{}
	spellings := map[string]string{}
	fold := cases.Fold()
	for side, paths := range [][]checkoutPath{local, remote} {
		for _, item := range paths {
			key := fold.String(item.path)
			if old, ok := spellings[key]; ok && old != item.path {
				return errMergeNameCollision
			}
			spellings[key] = item.path
			e := entries[item.path]
			if side == 0 {
				e.local = item
			} else {
				e.remote = item
			}
			entries[item.path] = e
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	// A file/directory conflict keeps the whole local subtree in a conflict copy.
	nested := map[string]bool{}
	for _, name := range names {
		if nested[path.Dir(name)] {
			nested[name] = true
			continue
		}
		e := entries[name]
		switch {
		case e.local.kind == "":
			p.Download.add(name)
		case e.remote.kind == "":
			p.Upload.add(name)
		case e.local.kind != e.remote.kind:
			nested[name] = true
			p.Conflict.add(name)
		case e.local.kind == "File" && e.local.id != e.remote.id:
			p.Conflict.add(name)
		}
	}
	return nil
}
