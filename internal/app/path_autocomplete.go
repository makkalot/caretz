package app

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/makkalot/caretz/internal/subagents"
)

const (
	maxPathAutocompleteResults  = 8
	largePathAutocompleteSet    = 20000
	maxLargeFuzzyScanCandidates = 50000
)

type pathCandidate struct {
	path      string
	lowerPath string
	baseLower string
	isDir     bool
	isAgent   bool
}

type pathMatch struct {
	pathCandidate
	score int
}

type pathAutocomplete struct {
	candidates []pathCandidate
	matches    []pathMatch
	query      string
	selected   int
	active     bool
	visible    bool

	// loaded delivers candidates discovered in the background. Indexing a
	// large non-git directory such as $HOME can take a long time, so the
	// prompt must not wait for it. A nil channel means candidates are ready.
	loaded        <-chan []pathCandidate
	allowedAgents map[string]bool
}

func newPathAutocomplete(root string, agentsDirs []string) pathAutocomplete {
	loaded := make(chan []pathCandidate, 1)
	go func() {
		loaded <- discoverMentionCandidates(root, agentsDirs)
	}()
	return pathAutocomplete{loaded: loaded}
}

// indexing reports whether background candidate discovery is still running.
func (a *pathAutocomplete) indexing() bool {
	a.poll()
	return a.loaded != nil
}

// poll installs background-discovered candidates once they are available
// without blocking the caller.
func (a *pathAutocomplete) poll() {
	if a.loaded == nil {
		return
	}
	select {
	case candidates := <-a.loaded:
		a.loaded = nil
		a.candidates = candidates
		a.applyAgentFilter()
	default:
	}
}

func discoverMentionCandidates(root string, agentsDirs []string) []pathCandidate {
	candidates := discoverPathCandidates(root)
	candidates = append(candidates, discoverSubagentCandidates(agentsDirs)...)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })
	return candidates
}

func discoverPathCandidates(root string) []pathCandidate {
	paths, err := gitProjectFiles(root)
	if err != nil {
		paths = walkProjectFiles(root)
	}

	entries := make(map[string]bool)
	for _, path := range paths {
		path = filepath.ToSlash(filepath.Clean(path))
		if path == "." || path == "" || path == ".git" || strings.HasPrefix(path, ".git/") || path == ".caretz" || strings.HasPrefix(path, ".caretz/") {
			continue
		}
		entries[path] = false
		for parent := filepath.ToSlash(filepath.Dir(path)); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(parent)) {
			entries[parent] = true
		}
	}

	candidates := make([]pathCandidate, 0, len(entries))
	for path, isDir := range entries {
		if isDir {
			path += "/"
		}
		candidates = append(candidates, newPathCandidate(path, isDir))
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })
	return candidates
}

func discoverSubagentCandidates(agentsDirs []string) []pathCandidate {
	defs, err := subagents.DiscoverFromDirs(agentsDirs)
	if err != nil {
		return nil
	}
	candidates := make([]pathCandidate, 0, len(defs))
	for _, def := range defs {
		candidate := newPathCandidate(def.Name, false)
		candidate.isAgent = true
		candidates = append(candidates, candidate)
	}
	return candidates
}

func (a *pathAutocomplete) filterAgents(allowed map[string]bool) {
	a.allowedAgents = allowed
	if a.loaded == nil {
		a.applyAgentFilter()
	}
}

func (a *pathAutocomplete) applyAgentFilter() {
	if a.allowedAgents == nil {
		return
	}
	out := a.candidates[:0]
	for _, candidate := range a.candidates {
		if !candidate.isAgent || a.allowedAgents[candidate.path] {
			out = append(out, candidate)
		}
	}
	a.candidates = out
}

func newPathCandidate(path string, isDir bool) pathCandidate {
	c := pathCandidate{path: path, isDir: isDir}
	c.ensureIndex()
	return c
}

func (c *pathCandidate) ensureIndex() {
	if c.lowerPath == "" && c.path != "" {
		c.lowerPath = strings.ToLower(c.path)
	}
	if c.baseLower == "" && c.path != "" {
		c.baseLower = strings.ToLower(filepath.Base(strings.TrimSuffix(c.path, "/")))
	}
}

func gitProjectFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), nil
}

// maxWalkedProjectFiles bounds the fallback walk used outside git
// repositories so running in a huge tree such as $HOME stays cheap.
var maxWalkedProjectFiles = 100000

func walkProjectFiles(root string) []string {
	var paths []string
	homeLibrary := ""
	if home, err := os.UserHomeDir(); err == nil {
		homeLibrary = filepath.Join(home, "Library")
	}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && (skipWalkDir(entry.Name()) || path == homeLibrary) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err == nil {
			paths = append(paths, rel)
		}
		if len(paths) >= maxWalkedProjectFiles {
			return filepath.SkipAll
		}
		return nil
	})
	return paths
}

// skipWalkDir reports directories the non-git walk never descends into:
// hidden directories (VCS metadata, caches, tool state) and dependency trees.
func skipWalkDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "__pycache__"
}

func activePathQuery(value string, line, column int) (string, int, bool) {
	lines := strings.Split(value, "\n")
	if line < 0 || line >= len(lines) {
		return "", 0, false
	}
	runes := []rune(lines[line])
	if column < 0 || column > len(runes) {
		return "", 0, false
	}
	for i := column - 1; i >= 0; i-- {
		if unicode.IsSpace(runes[i]) {
			break
		}
		if runes[i] != '@' {
			continue
		}
		if i > 0 && !unicode.IsSpace(runes[i-1]) {
			return "", 0, false
		}
		return string(runes[i+1 : column]), column - i, true
	}
	return "", 0, false
}

func (a *pathAutocomplete) update(value string, line, column int) {
	a.poll()
	query, _, active := activePathQuery(value, line, column)
	if !active {
		a.dismiss()
		return
	}
	if !a.active || query != a.query {
		a.selected = 0
		a.visible = false
	}
	a.active = true
	a.query = query
	a.matches = rankPathCandidates(a.candidates, query, maxPathAutocompleteResults)
	if len(a.matches) == 0 {
		a.selected = 0
	} else if a.selected >= len(a.matches) {
		a.selected = len(a.matches) - 1
	}
}

func (a *pathAutocomplete) dismiss() {
	a.active = false
	a.query = ""
	a.matches = nil
	a.selected = 0
	a.visible = false
}

func (a *pathAutocomplete) show() bool {
	if !a.active || len(a.matches) == 0 {
		return false
	}
	a.visible = true
	return true
}

func (a *pathAutocomplete) move(delta int) bool {
	if !a.visible || len(a.matches) == 0 {
		return false
	}
	a.selected = (a.selected + delta + len(a.matches)) % len(a.matches)
	return true
}

func (a pathAutocomplete) selection() (string, bool) {
	if !a.active || len(a.matches) == 0 {
		return "", false
	}
	return a.matches[a.selected].path, true
}

func rankPathCandidates(candidates []pathCandidate, query string, limit int) []pathMatch {
	if limit <= 0 {
		return nil
	}
	query = strings.ToLower(query)
	if query == "" {
		if len(candidates) < limit {
			limit = len(candidates)
		}
		matches := make([]pathMatch, 0, limit)
		for i := range limit {
			matches = append(matches, pathMatch{pathCandidate: hydratePathCandidate(candidates[i])})
		}
		return matches
	}
	if len(candidates) > largePathAutocompleteSet {
		if matches := rankLargePathCandidates(candidates, query, limit); len(matches) > 0 {
			return matches
		}
	}

	matches := make([]pathMatch, 0, limit)
	for _, candidate := range candidates {
		candidate = hydratePathCandidate(candidate)
		score, ok := fuzzyPathScore(candidate.lowerPath, candidate.baseLower, query)
		if ok {
			matches = insertPathMatch(matches, pathMatch{pathCandidate: candidate, score: score}, limit)
		}
	}
	return matches
}

func rankLargePathCandidates(candidates []pathCandidate, query string, limit int) []pathMatch {
	matches := make([]pathMatch, 0, limit)
	seen := make(map[string]bool)
	add := func(candidate pathCandidate, score int) {
		if seen[candidate.path] {
			return
		}
		seen[candidate.path] = true
		matches = insertPathMatch(matches, pathMatch{pathCandidate: candidate, score: score}, limit)
	}
	pathQuery := strings.Contains(query, "/")
	for _, candidate := range candidates {
		candidate = hydratePathCandidate(candidate)
		switch {
		case strings.HasPrefix(candidate.baseLower, query):
			add(candidate, 1000-len(candidate.path))
		case pathQuery && strings.HasPrefix(candidate.lowerPath, query):
			add(candidate, 900-len(candidate.path))
		}
	}
	if len(matches) >= limit || len(query) < 2 {
		return matches
	}

	for _, candidate := range candidates {
		candidate = hydratePathCandidate(candidate)
		target := candidate.baseLower
		if pathQuery {
			target = candidate.lowerPath
		}
		if strings.Contains(target, query) {
			add(candidate, 700-len(candidate.path))
		}
	}
	if len(matches) >= limit {
		return matches
	}

	scanned := 0
	for _, candidate := range candidates {
		if scanned >= maxLargeFuzzyScanCandidates {
			break
		}
		scanned++
		candidate = hydratePathCandidate(candidate)
		score, ok := fuzzyPathScore(candidate.lowerPath, candidate.baseLower, query)
		if ok {
			add(candidate, score)
		}
	}
	return matches
}

func hydratePathCandidate(candidate pathCandidate) pathCandidate {
	candidate.ensureIndex()
	return candidate
}

func insertPathMatch(matches []pathMatch, match pathMatch, limit int) []pathMatch {
	insertAt := sort.Search(len(matches), func(i int) bool {
		if matches[i].score != match.score {
			return matches[i].score < match.score
		}
		return matches[i].path > match.path
	})
	if insertAt >= limit {
		return matches
	}
	matches = append(matches, pathMatch{})
	copy(matches[insertAt+1:], matches[insertAt:])
	matches[insertAt] = match
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func fuzzyPathScore(candidate, base, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	target := base
	if strings.Contains(query, "/") {
		target = candidate
	}
	// One- and two-character searches are too ambiguous for subsequence
	// matching. Requiring a contiguous filename (or explicit path) match keeps
	// short queries such as "cp" from pulling in vendor paths merely because a
	// "c" and a later "p" happen to occur somewhere in the path.
	if len([]rune(query)) <= 2 {
		index := strings.Index(target, query)
		if index < 0 {
			return 0, false
		}
		score := 100 - index*5 - len(candidate)
		if index == 0 {
			score += 40
		}
		return score, true
	}
	queryRunes := []rune(query)
	qi, score, last := 0, 0, -2
	for i, r := range target {
		if qi >= len(queryRunes) || r != queryRunes[qi] {
			continue
		}
		score += 10
		if i == 0 || target[i-1] == '/' {
			score += 20
		}
		if i == last+1 {
			score += 8
		}
		last = i
		qi++
	}
	if qi != len(queryRunes) {
		return 0, false
	}
	if strings.HasPrefix(target, query) {
		score += 40
	}
	if strings.HasPrefix(base, query) {
		score += 30
	}
	return score - len(candidate), true
}
