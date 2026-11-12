package main

import (
	"errors"
	"fleet/contract"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type attemptContext struct {
	Core    string
	Index   string
	Sources []any
}

func sourceRemoteIdentity(remote string) string {
	if strings.HasPrefix(remote, "git@") {
		remote = "ssh://" + strings.Replace(remote, ":", "/", 1)
	}
	u, err := url.Parse(remote)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "ssh") || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	if u.User != nil {
		if _, password := u.User.Password(); password || u.Scheme == "https" {
			return ""
		}
	}
	host := strings.ToLower(u.Hostname())
	if u.Port() != "" {
		host += ":" + u.Port()
	}
	return host + strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
}

// resolveAttemptContext never fetches, reads working-tree rule files, or executes source hooks/filters.
// Empty inputDir checks the same sources for readiness without materializing files.
func resolveAttemptContext(project any, host string, roots, forbidden []string, inputDir string) (attemptContext, error) {
	out := attemptContext{Sources: []any{}}
	value := jAt(project, "contextSources")
	if value == nil {
		return out, nil
	}
	sources, err := contract.ContextSources(value)
	if err != nil {
		return out, errors.New("instruction-source-invalid: " + err.Error())
	}
	for _, source := range sources {
		fail := func(reason string) (attemptContext, error) {
			return out, errors.New("instruction-source-unavailable: " + source.Repo + "@" + source.Revision + ": " + reason)
		}
		repo := gitRepo(project, source.Repo)
		matches := 0
		for _, candidate := range jArr(project, "repos") {
			if jStr(candidate, "id") == source.Repo {
				matches++
			}
		}
		if matches != 1 {
			return fail("one declared repository identity required")
		}
		if repo == nil || sourceRemoteIdentity(jStr(repo, "remote")) == "" {
			return fail("declared repository identity required")
		}
		checkout := ""
		for _, env := range jArr(project, "environments") {
			if jStr(env, "hostId") == host && jStr(env, "repo") == source.Repo {
				if checkout != "" {
					return fail("ambiguous host binding")
				}
				checkout = jStr(env, "checkoutPath")
			}
		}
		root, err := canon(checkout)
		if checkout == "" || err != nil || !filepath.IsAbs(checkout) || overlaps(root, forbidden) {
			return fail("eligible source checkout binding required")
		}
		allowed := false
		for _, base := range roots {
			if under(root, base) {
				allowed = true
			}
		}
		if !allowed {
			return fail("source checkout outside allowed roots")
		}
		git := func(args ...string) ([]byte, error) {
			c := hardenedGit(append([]string{"--no-replace-objects", "--no-lazy-fetch", "--literal-pathspecs", "-C", root}, args...)...)
			c.Env = append(c.Env, "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
			return c.Output()
		}
		common, err := git("rev-parse", "--git-common-dir")
		if err != nil {
			return fail("source Git metadata unavailable")
		}
		configDir := strings.TrimSpace(string(common))
		if !filepath.IsAbs(configDir) {
			configDir = filepath.Join(root, configDir)
		}
		configDir, err = canon(configDir)
		if err != nil || overlaps(configDir, forbidden) {
			return fail("source Git metadata outside authority")
		}
		allowed = false
		for _, base := range roots {
			if under(configDir, base) {
				allowed = true
			}
		}
		if !allowed {
			return fail("source Git metadata outside allowed roots")
		}
		for _, metadata := range []string{configDir, filepath.Join(configDir, "config")} {
			st, e := os.Lstat(metadata)
			if e != nil || st.Mode()&os.ModeSymlink != 0 || owner(st) != os.Getuid() {
				return fail("source Git metadata must be host-user owned and not symlinked")
			}
		}
		// The supported layout is local owned objects, not recursive alternates or symlinked storage.
		for _, query := range [][]string{{"rev-parse", "--absolute-git-dir"}, {"rev-parse", "--git-path", "objects"}} {
			value, e := git(query...)
			if e != nil {
				return fail("source Git storage unavailable")
			}
			storage := strings.TrimSpace(string(value))
			if !filepath.IsAbs(storage) {
				storage = filepath.Join(root, storage)
			}
			resolved, e := canon(storage)
			eligible := false
			for _, base := range roots {
				if under(resolved, base) {
					eligible = true
				}
			}
			if e != nil || !eligible || overlaps(resolved, forbidden) || resolved != filepath.Clean(storage) {
				return fail("source Git storage outside supported authority")
			}
			st, e := os.Stat(resolved)
			if e != nil || !st.IsDir() || owner(st) != os.Getuid() {
				return fail("source Git storage must be host-user owned")
			}
			if query[len(query)-1] != "objects" {
				continue
			}
			if e := filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, e error) error {
				if e != nil {
					return e
				}
				info, e := entry.Info()
				if e != nil {
					return e
				}
				if info.Mode()&os.ModeSymlink != 0 || owner(info) != os.Getuid() || (!info.IsDir() && !info.Mode().IsRegular()) {
					return errors.New("unsupported object storage")
				}
				if path == filepath.Join(resolved, "info", "alternates") || path == filepath.Join(resolved, "info", "http-alternates") {
					return errors.New("alternate object storage unsupported")
				}
				return nil
			}); e != nil {
				return fail("source Git object symlinks, alternates or foreign ownership unsupported")
			}
		}
		origin, err := git("config", "--no-includes", "--file", filepath.Join(configDir, "config"), "--get", "remote.origin.url")
		if err != nil || sourceRemoteIdentity(strings.TrimSpace(string(origin))) != sourceRemoteIdentity(jStr(repo, "remote")) {
			return fail("source origin identity mismatch")
		}
		kind, err := git("cat-file", "-t", source.Revision)
		if err != nil || strings.TrimSpace(string(kind)) != "commit" {
			return fail("exact commit missing; stage declared revision before launch")
		}
		for group, files := range [][]contract.ContextSourceFile{source.Core, source.Linked} {
			for _, file := range files {
				tree, err := git("ls-tree", "-z", source.Revision, "--", file.Path)
				if err != nil {
					return fail("declared blob unavailable: " + file.Path)
				}
				entries := strings.Split(strings.TrimSuffix(string(tree), "\x00"), "\x00")
				if len(entries) != 1 {
					return fail("expected one regular blob: " + file.Path)
				}
				pair := strings.SplitN(entries[0], "\t", 2)
				if len(pair) != 2 || pair[1] != file.Path {
					return fail("declared blob missing: " + file.Path)
				}
				meta := strings.Fields(pair[0])
				if len(meta) != 3 || (meta[0] != "100644" && meta[0] != "100755") || meta[1] != "blob" {
					return fail("not a regular Git blob: " + file.Path)
				}
				bytes, err := git("cat-file", "blob", meta[2])
				if err != nil || !utf8.Valid(bytes) || strings.ContainsRune(string(bytes), 0) || sha(bytes) != file.Sha256 {
					return fail("blob digest/text mismatch: " + file.Path)
				}
				rel := filepath.ToSlash(filepath.Join("context-sources", source.Repo, source.Revision, file.Path))
				if inputDir != "" {
					if err := stageContextFile(inputDir, rel, bytes); err != nil {
						return out, err
					}
				}
				role := "linked"
				if group == 0 {
					role = "core"
					out.Core += "\n\n# Shared source " + source.Repo + "@" + source.Revision + ":" + file.Path + " SHA256 " + file.Sha256 + "\n\n" + string(bytes)
				} else {
					out.Index += "\n- " + rel + " SHA256 " + file.Sha256
				}
				out.Sources = append(out.Sources, M{"repo": source.Repo, "revision": source.Revision, "path": file.Path, "sha256": file.Sha256, "inputPath": rel, "role": role})
			}
		}
	}
	if len(out.Sources) > 0 {
		out.Core = "Fleet resolved source context: exact declared revisions below override ambient/latest source selection. Project contracts/rules override shared rules on conflict; retain and report conflicting pins rather than claiming both were read.\n" + out.Core
		if out.Index != "" {
			out.Index = "\n\nLinked-only source index (read on demand under FLEET_INPUT_DIR):" + out.Index
		}
	}
	return out, nil
}

// Runtime assembly follows definition validation; resolved sources do not loosen the profile input contract.
func assembledInstructions(profile, skills any, projectInstructions string, context attemptContext) string {
	text := jStr(withSkills(profile, skills), "instructions")
	if context.Core != "" {
		text = context.Core + "\n\n# Selected profile and skills\n" + text + context.Index
	}
	if projectInstructions != "" {
		text += "\n\n# Frozen project context (overrides shared rules on conflict)\n" + projectInstructions
	}
	return text
}

func stageContextFile(input, rel string, data []byte) error {
	root, err := os.OpenRoot(input)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.MkdirAll(filepath.Dir(rel), 0700); err != nil {
		return err
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		st, e := root.Lstat(rel)
		if e != nil || !st.Mode().IsRegular() {
			return errors.New("instruction-source-input-conflict")
		}
		old, e := root.Open(rel)
		if e != nil {
			return e
		}
		defer old.Close()
		b, e := io.ReadAll(old)
		if e != nil || sha(b) != sha(data) {
			return errors.New("instruction-source-input-conflict")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func prepareInstructions(cmd *exec.Cmd, harness string, p M, projectInstructions string, context attemptContext, evidence M) (M, M) {
	base, hasProfile := p["agentConfig"]
	if !hasProfile {
		base = p
	}
	text := assembledInstructions(base, p["skills"], projectInstructions, context)
	// A private runtime copy can carry verified source bodies beyond the strict profile-definition text limit.
	runtime := jObj(jClone(p))
	if hasProfile {
		cfg := jObj(jClone(base))
		cfg["instructions"] = text
		runtime["agentConfig"] = cfg
	} else {
		runtime["instructions"] = text
	}
	applyResolvedInstructions(cmd, harness, text)
	if evidence == nil {
		evidence = M{"requested": base, "applied": M{"harness": harness}, "profileSha256": profileHash(base)}
	}
	evidence["selection"] = M{"harness": harness, "account": p["account"], "authority": "trusted host account reference; native observation follows"}
	applied := jObj(evidence["applied"])
	if applied == nil {
		applied = M{}
		evidence["applied"] = applied
	}
	applied["instructions"] = text
	evidence["instructionsSha256"] = sha([]byte(text))
	evidence["resolvedContextSources"] = context.Sources
	evidence["contextDelivery"] = "explicit resolved instruction payload; unchanged core may repeat on resume without durable native loaded acknowledgement"
	return runtime, evidence
}
