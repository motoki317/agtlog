package source

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"

	"github.com/motoki317/agtlog/internal/model"
)

type mirrorIdentity struct {
	agent model.AgentKind
	id    string
}

type sourceDigest struct {
	sum [sha256.Size]byte
	ok  bool
}

type mirrorCandidates struct {
	identities map[mirrorIdentity]bool
	paths      map[string]bool
}

type mirrorFollowSnapshot struct {
	sessions     followSessionIndex
	visiblePaths map[string]bool
	candidates   mirrorCandidates
}

type mirrorFollowState struct {
	current atomic.Pointer[mirrorFollowSnapshot]
}

// collapseMirroredSessionsWithCandidatesContext must run before graph linking,
// because duplicate copies of one child session make child lookup ambiguous.
// Copies that share an agent and session ID collapse only when their parsed
// state and source digests match. The returned candidates name the collapsed
// identities and source paths, so that the follower knows which changes need a
// snapshot rebuild. See docs/ADR/20260823-extra-agent-home-directories.md.
func collapseMirroredSessionsWithCandidatesContext(ctx context.Context, sessions []*model.Session) ([]*model.Session, mirrorCandidates, error) {
	collapsed := make([]*model.Session, 0, len(sessions))
	byIdentity := make(map[mirrorIdentity][]int)
	digests := make(map[string]sourceDigest)
	mirroredIdentities := make(map[mirrorIdentity]bool)
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return nil, mirrorCandidates{}, err
		}
		if session == nil || session.ID == "" {
			collapsed = append(collapsed, session)
			continue
		}
		identity := mirrorIdentity{agent: session.Agent, id: session.ID}
		matched := false
		for _, index := range byIdentity[identity] {
			equal, err := mirroredSessionsEqualContext(ctx, collapsed[index], session, digests)
			if err != nil {
				return nil, mirrorCandidates{}, err
			}
			if !equal {
				continue
			}
			if filepath.Clean(session.Path) < filepath.Clean(collapsed[index].Path) {
				collapsed[index] = session
			}
			mirroredIdentities[identity] = true
			matched = true
			break
		}
		if matched {
			continue
		}
		byIdentity[identity] = append(byIdentity[identity], len(collapsed))
		collapsed = append(collapsed, session)
	}
	candidates := mirrorCandidates{identities: mirroredIdentities}
	if len(mirroredIdentities) > 0 {
		candidates.paths = make(map[string]bool)
		for _, session := range sessions {
			if session == nil || !mirroredIdentities[mirrorIdentity{agent: session.Agent, id: session.ID}] {
				continue
			}
			collectMirrorSourcePaths(session, candidates.paths)
		}
	}
	return collapsed, candidates, nil
}

func collectMirrorSourcePaths(session *model.Session, paths map[string]bool) {
	if session == nil {
		return
	}
	path := session.Path
	if separator := strings.IndexByte(path, '#'); separator >= 0 {
		path = path[:separator]
	}
	if path != "" {
		paths[filepath.Clean(path)] = true
	}
	for _, child := range session.Subagents {
		collectMirrorSourcePaths(child, paths)
	}
}

func (candidates mirrorCandidates) affects(sessions []*model.Session, removedPaths []string) bool {
	for _, path := range removedPaths {
		if candidates.paths[filepath.Clean(path)] {
			return true
		}
	}
	for _, session := range sessions {
		if session != nil && candidates.identities[mirrorIdentity{agent: session.Agent, id: session.ID}] {
			return true
		}
	}
	return false
}

func (state *mirrorFollowState) publish(parsedSessions, visibleSessions []*model.Session, candidates mirrorCandidates) {
	snapshot := &mirrorFollowSnapshot{
		visiblePaths: sessionPathSet(visibleSessions),
		candidates:   candidates,
	}
	if len(candidates.identities) > 0 {
		snapshot.sessions = indexFollowSessions(parsedSessions)
	}
	state.current.Store(snapshot)
}

func (snapshot *mirrorFollowSnapshot) cloneSessions() followSessionIndex {
	if snapshot == nil || snapshot.sessions == nil {
		return nil
	}
	cloned := make(followSessionIndex, len(snapshot.sessions))
	for path, session := range snapshot.sessions {
		cloned[path] = session
	}
	return cloned
}

func mirroredSessionsEqualContext(ctx context.Context, left, right *model.Session, digests map[string]sourceDigest) (bool, error) {
	if left.SourceSize != right.SourceSize {
		return false, nil
	}
	if !reflect.DeepEqual(mirrorComparable(left), mirrorComparable(right)) {
		return false, nil
	}
	return mirrorSourcesEqualContext(ctx, left, right, digests)
}

func mirrorSourcesEqualContext(ctx context.Context, left, right *model.Session, digests map[string]sourceDigest) (bool, error) {
	leftDigest, err := digestSessionSourceContext(ctx, left, digests)
	if err != nil {
		return false, err
	}
	rightDigest, err := digestSessionSourceContext(ctx, right, digests)
	if err != nil {
		return false, err
	}
	if !leftDigest.ok || !rightDigest.ok || leftDigest.sum != rightDigest.sum {
		return false, nil
	}
	for index := range left.Subagents {
		equal, err := mirrorSourcesEqualContext(ctx, left.Subagents[index], right.Subagents[index], digests)
		if err != nil || !equal {
			return false, err
		}
	}
	return true, nil
}

// digestSessionSourceContext reports a read failure as a digest with ok false,
// so the copies do not collapse. Only cancellation returns an error.
func digestSessionSourceContext(ctx context.Context, session *model.Session, digests map[string]sourceDigest) (sourceDigest, error) {
	if err := ctx.Err(); err != nil {
		return sourceDigest{}, err
	}
	path := session.Path
	// The part before # is the physical file. The suffix names a logical node inside it.
	if separator := strings.IndexByte(path, '#'); separator >= 0 {
		path = path[:separator]
	}
	if path == "" {
		return sourceDigest{}, nil
	}
	path = filepath.Clean(path)
	if digest, exists := digests[path]; exists {
		return digest, nil
	}
	file, err := os.Open(path)
	if err != nil {
		digests[path] = sourceDigest{}
		return sourceDigest{}, nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		digests[path] = sourceDigest{}
		return sourceDigest{}, nil
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, &contextReader{ctx: ctx, reader: file}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return sourceDigest{}, ctxErr
		}
		digests[path] = sourceDigest{}
		return sourceDigest{}, nil
	}
	var digest sourceDigest
	copy(digest.sum[:], hash.Sum(nil))
	digest.ok = true
	digests[path] = digest
	return digest, nil
}

// mirrorComparable clears the physical paths, which differ by home, and the
// Duplicated fields, which attribution recomputes after linking. It clears
// Event.Subagent pointers because the copy.Subagents comparison below covers
// those trees.
func mirrorComparable(session *model.Session) *model.Session {
	copy := *session
	copy.Path = ""
	copy.DuplicatedUSD = 0
	copy.DuplicatedUsage = model.Usage{}
	copy.DuplicatedCount = 0
	copy.DuplicatedByModel = nil
	copy.DuplicatedOwners = nil
	if len(session.Events) > 0 {
		copy.Events = append([]model.Event(nil), session.Events...)
		for index := range copy.Events {
			copy.Events[index].RecordRef.Path = ""
			copy.Events[index].Subagent = nil
		}
	}
	if len(session.Subagents) > 0 {
		copy.Subagents = make([]*model.Session, 0, len(session.Subagents))
		for _, child := range session.Subagents {
			copy.Subagents = append(copy.Subagents, mirrorComparable(child))
		}
	}
	return &copy
}
