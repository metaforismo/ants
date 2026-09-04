package scm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/metaforismo/ants/internal/domain"
)

// Memory is the deterministic in-process SCM implementation used for hermetic
// tests and demo replays when no git binary is wanted. It maintains a real
// commit graph (parents, merge bases) so branching and three-way merging have
// the same observable semantics as local_git.
//
// Known granularity divergence: Memory resolves merge conflicts at file level
// while local_git delegates to git's line-level merge. Contract tests assert
// only semantics that hold under both. Anything finer belongs to the real
// driver.
type Memory struct {
	mu    sync.Mutex
	repos map[string]*memoryRepo

	seq int64 // commit creation order; breaks merge-base ties deterministically
}

var _ Driver = (*Memory)(nil)

type memCommit struct {
	sha     string
	parents []string
	seq     int64
	files   map[string][]byte
}

type memoryRepo struct {
	defaultBranch string
	branches      map[string]string // branch name -> tip SHA
	commits       map[string]*memCommit
}

func NewMemory() *Memory {
	return &Memory{repos: map[string]*memoryRepo{}}
}

func (m *Memory) Name() string { return "memory" }

func repoKey(h Handle) string { return h.Root + "\x00" + string(h.SandboxID) }

func (m *Memory) Init(_ context.Context, h Handle, seed Seed) error {
	if seed.DefaultBranch == "" {
		return domain.Invalidf("scm_seed_branch", "seed requires a default branch")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := repoKey(h)
	if _, exists := m.repos[key]; exists {
		return domain.Conflictf("scm_repo_exists", "repository already initialized at %s", key)
	}
	repo := &memoryRepo{
		defaultBranch: seed.DefaultBranch,
		branches:      map[string]string{},
		commits:       map[string]*memCommit{},
	}
	rootCommit := m.newCommit(fmt.Sprintf("seed %s", key), nil, seed.Files)
	repo.commits[rootCommit.sha] = rootCommit
	repo.branches[seed.DefaultBranch] = rootCommit.sha
	m.repos[key] = repo
	return nil
}

func (m *Memory) Head(_ context.Context, h Handle, branch string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo, err := m.repo(h)
	if err != nil {
		return "", err
	}
	sha, ok := repo.branches[branch]
	if !ok {
		return "", domain.NotFoundf("branch", branch)
	}
	return sha, nil
}

func (m *Memory) CreateBranch(_ context.Context, h Handle, name, fromBranch string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo, err := m.repo(h)
	if err != nil {
		return err
	}
	from, ok := repo.branches[fromBranch]
	if !ok {
		return domain.NotFoundf("branch", fromBranch)
	}
	if _, exists := repo.branches[name]; exists {
		return domain.Conflictf("scm_branch_exists", "branch %q already exists", name)
	}
	repo.branches[name] = from
	return nil
}

func (m *Memory) CommitFiles(_ context.Context, h Handle, branch, message string, files map[string][]byte) (CommitResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo, err := m.repo(h)
	if err != nil {
		return CommitResult{}, err
	}
	tip, ok := repo.branches[branch]
	if !ok {
		return CommitResult{}, domain.NotFoundf("branch", branch)
	}
	tree := copyFiles(repo.commits[tip].files)
	for path, content := range files {
		if !safePath(path) {
			return CommitResult{}, domain.Invalidf("scm_path", "file path %q must be relative and contained", path)
		}
		if content == nil {
			delete(tree, path)
			continue
		}
		tree[path] = append([]byte(nil), content...)
	}
	commit := m.newCommit(message, []string{tip}, tree)
	repo.commits[commit.sha] = commit
	repo.branches[branch] = commit.sha
	return CommitResult{SHA: commit.sha, Head: commit.sha}, nil
}

func (m *Memory) Merge(_ context.Context, h Handle, targetBranch, sourceBranch, message string) (MergeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo, err := m.repo(h)
	if err != nil {
		return MergeResult{}, err
	}
	oursTip, ok := repo.branches[targetBranch]
	if !ok {
		return MergeResult{}, domain.NotFoundf("branch", targetBranch)
	}
	theirsTip, ok := repo.branches[sourceBranch]
	if !ok {
		return MergeResult{}, domain.NotFoundf("branch", sourceBranch)
	}
	if oursTip == theirsTip {
		return MergeResult{SHA: oursTip}, nil
	}

	baseSHA := m.mergeBase(repo, oursTip, theirsTip)
	// If source is already an ancestor of target, merging it again is a
	// semantic no-op. Match real Git and preserve the target SHA.
	if baseSHA == theirsTip {
		return MergeResult{SHA: oursTip}, nil
	}
	baseFiles := map[string][]byte{}
	if baseSHA != "" {
		baseFiles = repo.commits[baseSHA].files
	}
	ours := repo.commits[oursTip].files
	theirs := repo.commits[theirsTip].files

	merged := copyFiles(ours)
	var conflicts []string
	paths := map[string]bool{}
	for p := range theirs {
		paths[p] = true
	}
	for p := range baseFiles {
		paths[p] = true
	}
	for _, p := range sortedKeys(paths) {
		b := baseFiles[p]
		o := ours[p]
		t := theirs[p]
		switch {
		case bytesEqual(t, b):
			// Source left it alone: keep our version (including deletion).
		case bytesEqual(o, b):
			// Only source changed it: take their version (or deletion).
			if t == nil {
				delete(merged, p)
			} else {
				merged[p] = append([]byte(nil), t...)
			}
		case bytesEqual(o, t):
			// Both arrived at the same content: nothing to do.
		default:
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		// Target stays exactly where it was: conflict resolution is a
		// planner decision, never a silent pick.
		return MergeResult{Conflicts: conflicts}, nil
	}

	commit := m.newCommit(message, []string{oursTip, theirsTip}, merged)
	repo.commits[commit.sha] = commit
	repo.branches[targetBranch] = commit.sha
	return MergeResult{SHA: commit.sha}, nil
}

func (m *Memory) Diff(_ context.Context, h Handle, baseSHA, headSHA string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo, err := m.repo(h)
	if err != nil {
		return nil, err
	}
	baseCommit, ok := repo.commits[baseSHA]
	if !ok {
		return nil, domain.NotFoundf("commit", baseSHA)
	}
	headCommit, ok := repo.commits[headSHA]
	if !ok {
		return nil, domain.NotFoundf("commit", headSHA)
	}
	return renderUnifiedDiff(baseCommit.files, headCommit.files), nil
}

func (m *Memory) Files(_ context.Context, h Handle, branch string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo, err := m.repo(h)
	if err != nil {
		return nil, err
	}
	tip, ok := repo.branches[branch]
	if !ok {
		return nil, domain.NotFoundf("branch", branch)
	}
	return copyFiles(repo.commits[tip].files), nil
}

func (m *Memory) repo(h Handle) (*memoryRepo, error) {
	repo, ok := m.repos[repoKey(h)]
	if !ok {
		return nil, domain.NotFoundf("repository", repoKey(h))
	}
	return repo, nil
}

func (m *Memory) newCommit(message string, parents []string, files map[string][]byte) *memCommit {
	m.seq++
	normalized := make([][]byte, 0, len(files))
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		normalized = append(normalized, files[p])
	}
	h := sha256.New()
	fmt.Fprintf(h, "seq:%d\nparents:%q\nmessage:%q\n", m.seq, parents, message)
	for i, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(normalized[i])
		h.Write([]byte{0})
	}
	return &memCommit{
		sha:     hex.EncodeToString(h.Sum(nil))[:40],
		parents: parents,
		seq:     m.seq,
		files:   copyFiles(files),
	}
}

// mergeBase returns the common ancestor with the highest creation sequence,
// which mirrors "most recent shared commit". Deterministic because seq is
// assigned monotonically under the repo mutex.
func (m *Memory) mergeBase(repo *memoryRepo, a, b string) string {
	ancestorsOfB := map[string]bool{}
	queue := []string{b}
	for len(queue) > 0 {
		cur := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if ancestorsOfB[cur] {
			continue
		}
		ancestorsOfB[cur] = true
		queue = append(queue, repo.commits[cur].parents...)
	}
	best := ""
	bestSeq := int64(-1)
	queue = []string{a}
	visited := map[string]bool{}
	for len(queue) > 0 {
		cur := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		if ancestorsOfB[cur] && repo.commits[cur].seq > bestSeq {
			best = cur
			bestSeq = repo.commits[cur].seq
		}
		queue = append(queue, repo.commits[cur].parents...)
	}
	return best
}

func bytesEqual(a, b []byte) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return string(a) == string(b)
}

func safePath(path string) bool {
	if strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.Contains(path, "\\") {
		return false
	}
	cleaned := strings.TrimPrefix(path, "./")
	return cleaned != ""
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func copyFiles(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}
