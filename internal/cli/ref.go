package cli

import (
	"errors"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/motoki317/agtlog/internal/model"
)

type graphNode struct {
	root    *model.Session
	session *model.Session
	ref     string
	path    string
}

func indexSessionGraphs(roots []*model.Session) []graphNode {
	nodes := make([]graphNode, 0, len(roots))
	for _, root := range roots {
		rootRef := canonicalRootRef(root)
		nodes = append(nodes, graphNode{root: root, session: root, ref: rootRef})
		appendChildNodes(&nodes, root, root, rootRef, "")
	}
	return nodes
}

func addressableRoots(roots []*model.Session, diagnostics []commandDiagnostic) ([]*model.Session, []commandDiagnostic) {
	invalid := make(map[*model.Session]string)
	for _, root := range roots {
		if root.ID == "" {
			invalid[root] = "session has no stable id"
		}
	}
	byRef := make(map[string]graphNode)
	for _, node := range indexSessionGraphs(roots) {
		if previous, exists := byRef[node.ref]; exists {
			invalid[previous.root] = "session graph contains a duplicate canonical ref"
			invalid[node.root] = "session graph contains a duplicate canonical ref"
			continue
		}
		byRef[node.ref] = node
	}
	valid := make([]*model.Session, 0, len(roots)-len(invalid))
	for _, root := range roots {
		message, rejected := invalid[root]
		if !rejected {
			valid = append(valid, root)
			continue
		}
		diagnostics = append(diagnostics, commandDiagnostic{agent: root.Agent, path: root.Path, err: errors.New(message), code: "unaddressable_session"})
	}
	slices.SortFunc(diagnostics, func(left, right commandDiagnostic) int {
		if left.agent != right.agent {
			return strings.Compare(string(left.agent), string(right.agent))
		}
		return strings.Compare(left.path, right.path)
	})
	return valid, diagnostics
}

func appendChildNodes(nodes *[]graphNode, root, parent *model.Session, rootRef, parentPath string) {
	for _, child := range parent.Subagents {
		path := canonicalChildPath(child, parentPath)
		*nodes = append(*nodes, graphNode{root: root, session: child, ref: rootRef + "#" + path, path: path})
		appendChildNodes(nodes, root, child, rootRef, path)
	}
}

func canonicalChildPath(child *model.Session, parentPath string) string {
	if child.AgentPath != "" {
		path := strings.Trim(child.AgentPath, "/")
		path = strings.TrimPrefix(path, "root/")
		if path != "" {
			return escapeRefPath(path)
		}
	}
	if _, suffix, ok := strings.Cut(child.Path, "#"); ok && suffix != "" {
		return escapeRefPath(strings.Trim(suffix, "/"))
	}
	segment := child.ID
	if segment == "" {
		segment = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(child.Path), "agent-"), filepath.Ext(child.Path))
	}
	segment = escapeRefComponent(segment)
	if parentPath == "" {
		return segment
	}
	return parentPath + "/" + segment
}

func escapeRefPath(path string) string {
	parts := strings.Split(path, "/")
	for index := range parts {
		parts[index] = escapeRefComponent(parts[index])
	}
	return strings.Join(parts, "/")
}

func escapeRefComponent(value string) string {
	return url.PathEscape(value)
}

func resolveSelector(selector string, nodes []graphNode, diagnostics []commandDiagnostic) (graphNode, error) {
	for _, node := range nodes {
		if node.ref == selector {
			return node, nil
		}
	}
	if filepath.IsAbs(selector) {
		matches := matchNodes(nodes, func(node graphNode) bool {
			return !strings.Contains(node.session.Path, "#") && filepath.Clean(node.session.Path) == filepath.Clean(selector)
		})
		if len(matches) > 0 {
			return uniqueSelector(selector, matches)
		}
		for _, diagnostic := range diagnostics {
			if filepath.Clean(diagnostic.path) != filepath.Clean(selector) {
				continue
			}
			if diagnostic.code == "unaddressable_session" {
				return graphNode{}, runtimeError("unaddressable_session", "the selected session cannot be addressed: "+diagnostic.err.Error())
			}
			if diagnostic.code == "unreadable_session" {
				return graphNode{}, runtimeError("unreadable_session", "the selected session could not be read")
			}
		}
	}
	exact := matchNodes(nodes, func(node graphNode) bool {
		return selectorEligible(node) && node.session.ID == selector
	})
	if len(exact) > 0 {
		return uniqueSelector(selector, exact)
	}
	if len([]rune(selector)) < 6 {
		return graphNode{}, usageError("a session id prefix must contain at least 6 characters")
	}
	prefix := matchNodes(nodes, func(node graphNode) bool {
		return selectorEligible(node) && strings.HasPrefix(node.session.ID, selector)
	})
	if len(prefix) > 0 {
		return uniqueSelector(selector, prefix)
	}
	return graphNode{}, resolutionError("not_found", "no session matches the selector", nil)
}

func selectorEligible(node graphNode) bool {
	if node.path == "" || !strings.Contains(node.session.Path, "#") {
		return true
	}
	parts := strings.Split(node.path, "/")
	return len(parts) == 0 || escapeRefComponent(node.session.ID) != parts[len(parts)-1]
}

func matchNodes(nodes []graphNode, matches func(graphNode) bool) []graphNode {
	result := make([]graphNode, 0)
	for _, node := range nodes {
		if matches(node) {
			result = append(result, node)
		}
	}
	return result
}

func uniqueSelector(selector string, nodes []graphNode) (graphNode, error) {
	if len(nodes) == 1 {
		return nodes[0], nil
	}
	candidates := make([]ErrorCandidate, 0, len(nodes))
	for _, node := range nodes {
		candidates = append(candidates, ErrorCandidate{
			Ref:       node.ref,
			Agent:     string(node.session.Agent),
			Project:   node.session.Project,
			Title:     node.session.Title,
			UpdatedAt: timestamp(node.session.UpdatedAt),
		})
	}
	slices.SortFunc(candidates, func(left, right ErrorCandidate) int { return strings.Compare(left.Ref, right.Ref) })
	return graphNode{}, resolutionError("ambiguous_ref", "the selector matches more than one session: "+selector, candidates)
}

func refForSession(nodes []graphNode, session *model.Session) string {
	for _, node := range nodes {
		if node.session == session {
			return node.ref
		}
	}
	return ""
}
