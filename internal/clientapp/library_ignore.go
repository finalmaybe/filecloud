package clientapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/syncignore"
	"golang.org/x/text/cases"
)

var errSyncIgnoreBlocked = errors.New("sync blocked by an ignored path; rename or move the blocking item and retry")
var errCheckoutIgnoreRulesChanged = errors.New("a pending conflict file is now ignored; temporarily remove the matching rules to finish checkout")

func ignoreSyncName(rules syncignore.Rules, name string) bool {
	// Protocol-reserved names must only be hidden by the journal that owns them.
	return rules.Match(name) && !strings.HasPrefix(cases.Fold().String(name), ".filecloud-internal-")
}

func ignoreSyncPath(rules syncignore.Rules, path string) bool {
	for _, name := range strings.Split(path, "/") {
		if ignoreSyncName(rules, name) {
			return true
		}
	}
	return false
}

func ignoredAncestors(ignored map[string]bool) map[string]bool {
	ancestors := make(map[string]bool)
	for path := range ignored {
		for parent, _ := splitFSActionPath(path); parent != "" && !ancestors[parent]; parent, _ = splitFSActionPath(parent) {
			ancestors[parent] = true
		}
	}
	return ancestors
}

// scanSyncSnapshot constructs a complete candidate tree, not the filtered view on disk.
// Excluded remote entries are inherited by object ID; their bytes never come from disk.
func scanSyncSnapshot(ctx context.Context, options bindOptions, baseRoot string) (worktreeSnapshot, error) {
	snapshot, err := scanWorktreeWithConfig(options.worktreeRoot, options.scanConfig)
	if err != nil || (!options.scanConfig.ignore.Active() && len(options.scanConfig.ignoreIndex) == 0) {
		return snapshot, err
	}
	if baseRoot != "" {
		cacheRoot, err := openVerifiedCacheRoot(options.clientDir)
		if err != nil {
			return worktreeSnapshot{}, err
		}
		defer cacheRoot.Close()
		options.cacheRoot = cacheRoot
	}
	merger := &_treeMerger{ctx: ctx, options: options, directories: make(map[string][]byte), seen: make(map[string]bool), budget: _newReplayBudget()}
	for _, value := range snapshot.objects {
		if value.kind == "directories" {
			merger.directories[value.id] = value.data
		}
	}
	view := ignoreSnapshotOverlay{
		snapshot: &snapshot, tree: merger, ancestors: ignoredAncestors(snapshot.ignored),
		shells: make(map[string]bool), staleCopyAncestors: make(map[string]bool),
	}
	snapshot.root, err = view.directory(baseRoot, snapshot.root, "", false, 0)
	snapshot.paths = slices.DeleteFunc(snapshot.paths, func(path checkoutPath) bool { return view.shells[path.path] })
	snapshot.omitted = omittedForIndex(snapshot.omitted, options.scanConfig)
	return snapshot, err
}

type ignoreSnapshotOverlay struct {
	snapshot           *worktreeSnapshot
	tree               *_treeMerger
	ancestors          map[string]bool
	shells             map[string]bool
	staleCopyAncestors map[string]bool
}

func (v ignoreSnapshotOverlay) directory(baseID, localID, parent string, rejoining bool, depth int) (string, error) {
	if depth > _mergeMaxDepth {
		return "", errors.New("sync ignore snapshot exceeds directory depth limit")
	}
	load := func(id string) (object.Directory, error) {
		if id == "" {
			return object.Directory{}, nil
		}
		return v.tree.loadDirectory(id)
	}
	base, err := load(baseID)
	if err != nil {
		return "", baselineReadError(err)
	}
	local, err := load(localID)
	if err != nil {
		return "", err
	}
	baseNames := _entriesByName(base)
	localNames := _entriesByName(local)
	rules := v.tree.options.scanConfig.ignore
	priorPaths := v.tree.options.scanConfig.ignoreIndex
	shadowed := make(map[string]string)
	fold := cases.Fold()
	entries := make([]scanEntry, 0, len(local.Entries))
	add := func(entry object.DirectoryEntry) {
		entries = append(entries, scanEntry{name: entry.Name, kind: entry.Type, id: entry.ID, modified: entry.ModifiedAt})
	}
	for _, entry := range base.Entries {
		path := joinScanPath(parent, entry.Name)
		localEntry := localNames[entry.Name]
		prior := priorPaths[path]
		ignored := ignoreSyncName(rules, entry.Name)
		// Compare participating children of rejoining directories, not their full IDs:
		// the local directory ID does not include children that are still ignored.
		compareChildren := localEntry != nil && localEntry.Type == "Directory" && entry.Type == "Directory"
		joining := (rejoining || prior.unmaterialized) && !compareChildren
		hydrate := joining && (localEntry == nil || prior.mtime != "" && sameIndexedEntry(prior, localEntry) && !_sameMergeEntry(localEntry, &entry))
		if joining && !ignored && !hydrate && !_sameMergeEntry(localEntry, &entry) &&
			(prior.mtime == "" || !sameIndexedEntry(prior, &entry)) && !sameEntryContent(localEntry, &entry) {
			return "", errSyncIgnoreBlocked
		}
		if ignored || hydrate {
			add(entry)
			shadowed[fold.String(entry.Name)] = entry.Name
			v.snapshot.omitted = append(v.snapshot.omitted, checkoutPath{path: path, kind: entry.Type, id: entry.ID, mtime: entry.ModifiedAt, unmaterialized: true})
			if !ignored {
				v.snapshot.needsCheckout = true
			}
		} else if localEntry == nil && entry.Type == "Directory" {
			id, err := v.directory(entry.ID, "", path, false, depth+1)
			if err != nil {
				return "", err
			}
			if id != "" {
				entry.ID = id
				add(entry)
			}
		}
	}
	for _, entry := range local.Entries {
		path := joinScanPath(parent, entry.Name)
		if shadow, ok := shadowed[fold.String(entry.Name)]; ok {
			if shadow != entry.Name {
				return "", errSyncIgnoreBlocked
			}
			continue
		}
		old := baseNames[entry.Name]
		prior := priorPaths[path]
		if old == nil && prior.unmaterialized && prior.mtime != "" && (prior.kind != "Directory" || entry.Type != "Directory") {
			if !sameIndexedEntry(prior, &entry) {
				return "", errSyncIgnoreBlocked
			}
			v.snapshot.needsCheckout = true
			v.snapshot.omitted = append(v.snapshot.omitted, prior)
			v.staleCopyAncestors[parent] = true
			continue
		}
		if entry.Type == "Directory" {
			oldID := ""
			joining := rejoining || prior.unmaterialized
			if old != nil && old.Type == "Directory" {
				oldID = old.ID
				// Ignored child operations and journal cleanup can change directory mtimes.
				if v.ancestors[path] || joining {
					entry.ModifiedAt = old.ModifiedAt
				}
			}
			entry.ID, err = v.directory(oldID, entry.ID, path, joining, depth+1)
			if err != nil {
				return "", err
			}
			if entry.ID == "" {
				// Only currently ignored shells must stay on disk. Stale shells stay
				// in snapshot.paths so checkout can journal their removal.
				if v.ancestors[path] {
					v.shells[path] = true
				}
				if v.staleCopyAncestors[path] {
					v.staleCopyAncestors[parent] = true
				}
				continue
			}
		} else if old != nil && old.Type == "Directory" {
			protected, err := v.directory(old.ID, "", path, false, depth+1)
			if err != nil {
				return "", err
			}
			if protected != "" {
				return "", errSyncIgnoreBlocked
			}
		}
		add(entry)
	}
	if parent != "" && len(entries) == 0 {
		if localID == "" {
			return "", nil
		}
		if baseID == "" {
			// A previously ignored directory can itself be the stale copy, even if it was empty.
			if prior := priorPaths[parent]; prior.unmaterialized && prior.mtime != "" && prior.kind == "Directory" && prior.id == localID {
				v.snapshot.needsCheckout = true
				v.snapshot.omitted = append(v.snapshot.omitted, prior)
				v.staleCopyAncestors[parent] = true
			}
			if v.ancestors[parent] || v.staleCopyAncestors[parent] {
				return "", nil
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	data, id, err := canonicalDirectory(parent, entries)
	if err != nil {
		return "", fmt.Errorf("construct sync ignore snapshot: %w", err)
	}
	if id != baseID && id != localID {
		v.tree.directories[id] = data
		v.snapshot.objects = append(v.snapshot.objects, scannedObject{"directories", id, data})
	}
	return id, nil
}

// A whole-tree merge can move or replace a directory. Verify that no such
// change removed, resurrected, or relocated any ignored remote subtree.
func verifyIgnoredPublication(ctx context.Context, options bindOptions, owner, remoteCommit, candidateRoot string) error {
	if !options.scanConfig.ignore.Active() {
		return nil
	}
	cache, err := openVerifiedCacheRoot(options.clientDir)
	if err != nil {
		return err
	}
	defer cache.Close()
	options.cacheRoot = cache
	remote, err := downloadTargetCommit(ctx, options, remoteCommit, owner)
	if err != nil {
		return err
	}
	tree := &_treeMerger{ctx: ctx, options: options, directories: make(map[string][]byte), seen: make(map[string]bool), budget: _newReplayBudget()}
	var compare func(string, string, int) error
	compare = func(left, right string, depth int) error {
		if left == right {
			return nil
		}
		if depth > _mergeMaxDepth {
			return errors.New("sync ignore snapshot exceeds directory depth limit")
		}
		entries := make(map[string][2]*object.DirectoryEntry)
		for side, id := range []string{left, right} {
			if id == "" {
				continue
			}
			directory, err := tree.loadDirectory(id)
			if err != nil {
				return err
			}
			for i := range directory.Entries {
				entry := &directory.Entries[i]
				pair := entries[entry.Name]
				pair[side] = entry
				entries[entry.Name] = pair
			}
		}
		for name, pair := range entries {
			if ignoreSyncName(options.scanConfig.ignore, name) {
				if !_sameMergeEntry(pair[0], pair[1]) {
					return errSyncIgnoreBlocked
				}
				continue
			}
			var roots [2]string
			for i, entry := range pair {
				if entry != nil && entry.Type == "Directory" {
					roots[i] = entry.ID
				}
			}
			if err := compare(roots[0], roots[1], depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return compare(remote.Root, candidateRoot, 0)
}

func sameIndexedEntry(prior checkoutPath, entry *object.DirectoryEntry) bool {
	return entry != nil && prior.kind == entry.Type && prior.id == entry.ID
}

func sameEntryContent(left, right *object.DirectoryEntry) bool {
	return left != nil && right != nil && left.Type == right.Type && left.ID == right.ID
}

func validateEmptyForBind(ctx context.Context, root *openedWorktree, config worktreeScanConfig) error {
	if !config.ignore.Active() {
		return root.validateEmpty()
	}
	snapshot, err := scanSyncSnapshot(ctx, bindOptions{worktreeRoot: root, scanConfig: config}, "")
	if err != nil {
		return err
	}
	if len(snapshot.paths) != 0 {
		return errNonemptyWorktree
	}
	return nil
}
