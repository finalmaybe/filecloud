package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
)

func fetchRestoreSource(ctx context.Context, options bindOptions, binding clientBinding, sourceID string) (historyInspectCommit, error) {
	client := historyInspectClient{binding: binding, base: options.base, token: options.token}
	commit, err := client.fetchCommit(ctx, sourceID)
	if err != nil {
		return historyInspectCommit{}, err
	}
	if commit.Role != "mainline" && commit.Role != "merge-source" {
		return historyInspectCommit{}, errors.New("restore source commit does not have a supported published history role")
	}
	return commit, nil
}

func planRestoreSnapshot(ctx context.Context, options bindOptions, snapshot worktreeSnapshot,
	source historyInspectCommit, sourcePath string, budget *restorePlanBudget) (restorePlan, error) {
	local := make(map[string][]byte, len(snapshot.objects))
	for _, value := range snapshot.objects {
		local[value.kind+"\x00"+value.id] = value.data
	}
	loader := func(kind, id string) ([]byte, error) {
		if data, ok := local[kind+"\x00"+id]; ok {
			return data, nil
		}
		return fetchRestoreObject(ctx, options, kind, id)
	}
	return planRestoreOverlay(restorePlanInput{CurrentRoot: snapshot.root, SourceRoot: source.Root,
		SourcePath: sourcePath, Load: loader, Budget: budget})
}

func fetchRestoreObject(ctx context.Context, options bindOptions, kind, id string) ([]byte, error) {
	if !object.ValidID(id) || (kind != "commits" && kind != "directories" && kind != "files") {
		return nil, errors.New("restore object reference is invalid")
	}
	request, err := authenticatedRequest(ctx, http.MethodGet,
		options.base.JoinPath("v1/libraries", options.libraryID, "objects", kind, id).String(), options.token, nil)
	if err != nil {
		return nil, err
	}
	status, data, _, err := doClientRequest(request)
	if err != nil {
		message := fmt.Sprintf("read restore %s object", kind)
		return nil, &transientRemoteError{message: message, cause: err}
	}
	if status != http.StatusOK {
		message := fmt.Sprintf("read restore %s object failed: server returned %s", kind, http.StatusText(status))
		return nil, remoteResponseError(message, status)
	}
	var verifyErr error
	switch kind {
	case "commits":
		_, verifyErr = object.VerifyCommit(data, id)
	case "directories":
		_, verifyErr = object.VerifyDirectory(data, id)
	case "files":
		_, verifyErr = object.VerifyFile(data, id)
	}
	if verifyErr != nil {
		return nil, fmt.Errorf("restore %s object is not canonical: %w", kind, verifyErr)
	}
	return data, nil
}

func canonicalRestoreCommit(owner, device, root, source, path string, parents []string, now time.Time) ([]byte, string, error) {
	message := "restore " + source + " " + path
	input, err := json.Marshal(struct {
		AuthorUserID string   `json:"AuthorUserId"`
		CreatedAt    string   `json:"CreatedAt"`
		DeviceID     string   `json:"DeviceId"`
		Message      string   `json:"Message"`
		Parents      []string `json:"Parents"`
		Root         string   `json:"Root"`
		Type         string   `json:"Type"`
		Version      int      `json:"Version"`
	}{AuthorUserID: owner, CreatedAt: canonicalProtocolMtime(now), DeviceID: device, Message: message,
		Parents: parents, Root: root, Type: "Commit", Version: 1})
	if err != nil {
		return nil, "", fmt.Errorf("construct restore commit: %w", err)
	}
	data, id, err := object.Canonicalize("commits", input)
	if err != nil {
		return nil, "", fmt.Errorf("construct restore commit: %w", err)
	}
	return data, id, nil
}
