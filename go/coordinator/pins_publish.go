package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"fleet/contract"
)

// pinsWriteConn: the connection whose key may write to taskqorg/fleet, and only to component pin proposals
// (docs/designs/fleet-host-updates.md, step 5): a fine-grained token with Commit statuses and Pull requests read and
// write. Its provider catalogue grants three tools; pinsPublish is its only writer.
const pinsWriteConn = "github-pins"

var pinsSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// pinsGatewayCall: a gateway call through pinsWriteConn as the CI project. A variable so tests answer without a gateway.
var pinsGatewayCall = func(ctx context.Context, a *App, tool string, args any) contract.CallLine {
	token, release, err := readTokenFor(ctx, a, pinsWriteConn, ciProject)
	if err != nil {
		code := err.Error()
		return contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: &code}
	}
	defer release()
	return gatewayCall(ctx, token, pinsWriteConn, tool, args, true)
}

// pinsWritable refuses every pull request but an open proposal of taskqorg/fleet: head automation/<kind>-pins in the
// same repository, base main, a full head SHA.
func pinsWritable(pr ghPull) error {
	if pinsKind(pr.Head.Ref) == "" || !strings.EqualFold(pr.Head.Repo.FullName, githubPublicRepo) || pr.Base.Ref != "main" || !pinsSHA.MatchString(pr.Head.SHA) || pr.NodeID == "" {
		return fmt.Errorf("pins write refused for %q (head %s:%s, base %q): only %s automation/<kind>-pins proposals to main", pr.URL, pr.Head.Repo.FullName, pr.Head.Ref, pr.Base.Ref, githubPublicRepo)
	}
	return nil
}

// pinsPublish records the host qualification on the proposal's head: qualify-hosts, and when it passed code-review
// (the proposal's review is its qualification) and auto-merge, so the merge queue takes it like any pull request.
func pinsPublish(ctx context.Context, a *App, pr ghPull, passed bool, description, targetURL string) error {
	if err := pinsWritable(pr); err != nil {
		return err
	}
	state := "failure"
	if passed {
		state = "success"
	}
	if len([]rune(description)) > 140 {
		description = string([]rune(description)[:137]) + "..."
	}
	call := func(tool string, args any) error {
		line := pinsGatewayCall(ctx, a, tool, args)
		if line.Outcome != contract.CallOutcomeOk {
			return fmt.Errorf("%s %s: %s %d", pinsWriteConn, tool, deref(line.ErrorCode), line.Status)
		}
		// GraphQL refuses with HTTP 200 and an errors list.
		var reply struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if json.Unmarshal(line.Output, &reply) == nil && len(reply.Errors) > 0 {
			return fmt.Errorf("%s %s: %s", pinsWriteConn, tool, reply.Errors[0].Message)
		}
		return nil
	}
	status := func(context, state, description string) error {
		return call("status_create", map[string]string{"owner": ciOwner, "repo": ciRepo, "sha": pr.Head.SHA, "state": state,
			"context": context, "description": description, "target_url": targetURL})
	}
	if err := status("qualify-hosts", state, description); err != nil || !passed {
		return err
	}
	return errors.Join(status("code-review", "success", "qualify-hosts, 0 findings fixed"),
		call("auto_merge_enable", pinsAutoMerge{Variables: pinsNode{ID: pr.NodeID}}))
}

// pinsAutoMerge: auto_merge_enable's arguments; the gateway injects the fixed mutation.
type pinsAutoMerge struct {
	Variables pinsNode `json:"variables"`
}

type pinsNode struct {
	ID string `json:"id"`
}
