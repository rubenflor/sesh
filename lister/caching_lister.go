package lister

import (
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/joshmedeski/sesh/v2/cache"
	"github.com/joshmedeski/sesh/v2/model"
)

const softTTL = 5 * time.Second

// CachingLister wraps a Lister with stale-while-revalidate caching for List().
// All other Lister methods delegate directly to the inner lister.
type CachingLister struct {
	inner         Lister
	cache         cache.Cache
	sshConfigured bool
	wg            sync.WaitGroup
}

// NewCachingLister creates a CachingLister that decorates inner with file-based caching.
// sshConfigured reports whether inner includes SSH in its default sources.
func NewCachingLister(inner Lister, c cache.Cache, sshConfigured bool) *CachingLister {
	return &CachingLister{inner: inner, cache: c, sshConfigured: sshConfigured}
}

// List implements Lister. It returns cached data when available, triggering a
// background refresh when the cache is older than the soft TTL.
// SSH-inclusive requests bypass caching. The cache stores local sources only;
// source filters, HideDuplicates, and HideAttached are applied after cache reads.
func (cl *CachingLister) List(opts ListOptions) (model.SeshSessions, error) {
	if slices.Contains(srcs(opts, cl.sshConfigured), "ssh") {
		return cl.inner.List(opts)
	}
	// Fetch/store the full local list (no dedup, no hide);
	// we apply all view-level filters ourselves after cache read.
	innerOpts := cl.localOptions(ListOptions{Context: opts.Context})

	cached, err := cl.cache.Read()
	// Old caches lack the ID or its server lifetime. Refetch before showing
	// their tmux rows; neither a name nor an unscoped ID can authorize deletion.
	hasSessionIdentities := true
	for _, key := range cached.Sessions.OrderedIndex {
		session := cached.Sessions.Directory[key]
		if session.Src == "tmux" && (session.TmuxSessionID == "" || session.TmuxServerGeneration == "") {
			hasSessionIdentities = false
			break
		}
	}
	if err == nil && hasSessionIdentities {
		age := time.Since(cached.Timestamp)
		if age < softTTL {
			slog.Debug("cache: hit (fresh)", "age", age)
			return cl.finish(cached.Sessions, opts)
		}
		// Stale -- return immediately but revalidate in background
		slog.Debug("cache: hit (stale, revalidating)", "age", age)
		cl.wg.Add(1)
		go func() {
			defer cl.wg.Done()
			cl.revalidate(innerOpts)
		}()
		return cl.finish(cached.Sessions, opts)
	}

	// Cold start -- fetch synchronously
	slog.Debug("cache: miss (cold start)")
	sessions, err := cl.inner.List(innerOpts)
	if err != nil {
		return sessions, err
	}
	if writeErr := cl.cache.Write(sessions); writeErr != nil {
		slog.Warn("cache: write failed on cold start", "error", writeErr)
	}
	return cl.finish(sessions, opts)
}

// finish applies view-level filtering and then display formatting without
// changing the raw session data stored in the cache.
func (cl *CachingLister) finish(sessions model.SeshSessions, opts ListOptions) (model.SeshSessions, error) {
	filtered := cl.applyFilters(sessions, opts)
	if !needsFormatting(opts) {
		return filtered, nil
	}
	return cl.inner.Format(filtered, opts)
}

// applyFilters applies view-level filters that should not affect what gets
// stored in the cache: source filtering, HideDuplicates, and HideAttached.
func (cl *CachingLister) applyFilters(sessions model.SeshSessions, opts ListOptions) model.SeshSessions {
	filtered := sessions.OrderedIndex

	// 1. Source filtering: if any source flag is set, keep only matching sessions.
	if allowed := sourceSet(opts); allowed != nil {
		result := make([]string, 0, len(filtered))
		for _, index := range filtered {
			if allowed[sessions.Directory[index].Src] {
				result = append(result, index)
			}
		}
		filtered = result
	}

	// 2. HideAttached runs before HideDuplicates so the attached tmux
	// session is removed from the dedup input. Otherwise tmux would win
	// the dedup against a same-named config/tmuxinator entry and then be
	// hidden, leaving no entry for the user to pick.
	if opts.HideAttached {
		attached, ok := cl.inner.GetAttachedTmuxSession()
		if ok {
			for i, index := range filtered {
				s := sessions.Directory[index]
				if s.Src == "tmux" && s.Name == attached.Name {
					filtered = slices.Delete(slices.Clone(filtered), i, i+1)
					break
				}
			}
		}
	}

	// 3. HideDuplicates: per-source dedup rules (see applyDedup).
	if opts.HideDuplicates {
		filtered = applyDedup(model.SeshSessions{
			OrderedIndex: filtered,
			Directory:    sessions.Directory,
		})
	}

	if len(filtered) == len(sessions.OrderedIndex) {
		return sessions
	}
	return model.SeshSessions{
		OrderedIndex: filtered,
		Directory:    sessions.Directory,
	}
}

// sourceSet returns a set of allowed source names based on opts, or nil if
// no source flags are set (meaning all sources are allowed).
func sourceSet(opts ListOptions) map[string]bool {
	if !opts.Tmux && !opts.Config && !opts.Zoxide && !opts.Tmuxinator && !opts.Panes && !opts.SSH {
		return nil
	}
	m := make(map[string]bool)
	if opts.Tmux {
		m["tmux"] = true
	}
	if opts.Config {
		m["config"] = true
	}
	if opts.Zoxide {
		m["zoxide"] = true
	}
	if opts.Tmuxinator {
		m["tmuxinator"] = true
	}
	if opts.Panes {
		m["tmux-pane"] = true
	}
	if opts.SSH {
		m["ssh"] = true
	}
	return m
}

// localOptions prevents configured defaults from selecting SSH during cache fills.
func (cl *CachingLister) localOptions(opts ListOptions) ListOptions {
	opts.SSH = false
	if cl.sshConfigured && sourceSet(opts) == nil {
		opts.Tmux, opts.Config, opts.Tmuxinator, opts.Zoxide = true, true, true, true
	}
	return opts
}

func (cl *CachingLister) revalidate(opts ListOptions) {
	sessions, err := cl.inner.List(cl.localOptions(opts))
	if err != nil {
		slog.Warn("cache: background revalidation fetch failed", "error", err)
		return
	}
	if err := cl.cache.Write(sessions); err != nil {
		slog.Warn("cache: background revalidation write failed", "error", err)
	}
}

// RefreshCache fetches live local data from the inner lister and writes it to the cache.
// This bypasses the cache read entirely, intended for use after sesh connect.
func (cl *CachingLister) RefreshCache(opts ListOptions) {
	cl.wg.Add(1)
	go func() {
		defer cl.wg.Done()
		cl.revalidate(opts)
	}()
}

// Wait blocks until all background refresh goroutines have completed.
// Call this before process exit to avoid truncated cache writes.
func (cl *CachingLister) Wait() {
	cl.wg.Wait()
}

// --- Delegate all other Lister methods to inner ---

func (cl *CachingLister) Format(sessions model.SeshSessions, opts ListOptions) (model.SeshSessions, error) {
	return cl.inner.Format(sessions, opts)
}

func (cl *CachingLister) ListTmuxPanes() (model.SeshSessions, error) {
	return cl.inner.ListTmuxPanes()
}

func (cl *CachingLister) FindTmuxSession(name string) (model.SeshSession, bool) {
	return cl.inner.FindTmuxSession(name)
}

func (cl *CachingLister) FindTmuxSessionByBase(base string) (model.SeshSession, bool) {
	return cl.inner.FindTmuxSessionByBase(base)
}

func (cl *CachingLister) GetAttachedTmuxSession() (model.SeshSession, bool) {
	return cl.inner.GetAttachedTmuxSession()
}

func (cl *CachingLister) GetLastTmuxSession() (model.SeshSession, bool) {
	return cl.inner.GetLastTmuxSession()
}

func (cl *CachingLister) FindConfigSession(name string) (model.SeshSession, bool) {
	return cl.inner.FindConfigSession(name)
}

func (cl *CachingLister) FindConfigWildcard(path string) (model.WildcardConfig, bool) {
	return cl.inner.FindConfigWildcard(path)
}

func (cl *CachingLister) FindZoxideSession(name string) (model.SeshSession, bool) {
	return cl.inner.FindZoxideSession(name)
}

func (cl *CachingLister) FindTmuxinatorConfig(name string) (model.SeshSession, bool) {
	return cl.inner.FindTmuxinatorConfig(name)
}
