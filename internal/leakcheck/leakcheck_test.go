package leakcheck

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

var allowlist = map[string]bool{
	"docker-desktop":   true,
	"minikube":         true,
	"kind":             true,
	"k3s":              true,
	"k3d":              true,
	"default":          true,
	"kube-system":      true,
	"kube-public":      true,
	"kube-node-lease":  true,
	"kubernetes":       true,
	"kubernetes-admin": true,
	"aws_profile":      true,
	"local":            true,
	"in-cluster":       true,
}

const minKubeconfigTokenLen = 5

var (
	accountID = regexp.MustCompile(`\b\d{12}\b`)
	arnTail   = regexp.MustCompile(`/([^/:\s]+)\z`)
)

func TestRepoFilesDoNotLeakLocalNames(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("repo root not found: %v", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}

	forbidden := collectForbidden(root)
	if len(forbidden) == 0 {
		t.Skip("no local identifiers present; nothing to guard")
	}

	var violations []string
	for _, token := range forbidden {
		files, err := gitGrep(root, token)
		if err != nil {
			t.Fatalf("git grep %q: %v", token, err)
		}
		if len(files) > 0 {
			violations = append(violations, fmt.Sprintf("%q -> %s", token, strings.Join(files, ", ")))
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("repo files leak local identifiers; scrub them and see AGENTS.md:\n  %s", strings.Join(violations, "\n  "))
	}
}

func collectForbidden(root string) []string {
	set := map[string]bool{}

	var add func(string)
	add = func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if strings.Contains(name, "arn:") {
			for _, id := range accountID.FindAllString(name, -1) {
				addToken(set, id, true)
			}
			if match := arnTail.FindStringSubmatch(name); match != nil {
				addToken(set, match[1], false)
			}
			return
		}
		addToken(set, name, false)
	}

	if cfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load(); err == nil {
		for name, context := range cfg.Contexts {
			add(name)
			add(context.Namespace)
		}
		for name := range cfg.Clusters {
			add(name)
		}
		for name := range cfg.AuthInfos {
			add(name)
		}
	}

	for _, token := range readDenylist(root) {
		if !allowlist[strings.ToLower(token)] {
			set[token] = true
		}
	}

	out := make([]string, 0, len(set))
	for token := range set {
		out = append(out, token)
	}
	sort.Strings(out)
	return out
}

func addToken(set map[string]bool, token string, isAccountID bool) {
	token = strings.TrimSpace(token)
	if token == "" || allowlist[strings.ToLower(token)] {
		return
	}
	if !isAccountID && len([]rune(token)) < minKubeconfigTokenLen {
		return
	}
	set[token] = true
}

func readDenylist(root string) []string {
	var tokens []string
	if env := os.Getenv("AGTLOG_LEAKCHECK_EXTRA"); env != "" {
		tokens = append(tokens, strings.FieldsFunc(env, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		})...)
	}

	file, err := os.Open(filepath.Join(root, ".leakcheck"))
	if err != nil {
		return tokens
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tokens = append(tokens, line)
	}
	return tokens
}

func gitGrep(root, token string) ([]string, error) {
	cmd := exec.Command("git", "grep", "-I", "-F", "-i", "-l", "--untracked", "-e", token,
		"--", ".", ":!go.sum", ":!flake.lock", ":!.leakcheck")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", filepath.Dir(file))
		}
		dir = parent
	}
}
