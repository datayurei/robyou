// Package catalog caches the course information seen during a run.
//
// The two course types have to be cached differently, because the server
// treats them differently: a public-elective search with an empty keyword
// returns the entire catalog, so it can be fetched in one pass, while an
// in-plan search with an empty keyword returns nothing at all. There is no way
// to enumerate the in-plan catalog, so it is accumulated instead — every search
// the program runs contributes whatever it saw, and the cache grows as more
// keywords are used.
//
// The cache is keyed by enrollment round (xkid): course IDs, seat counts and
// even the course list itself are only meaningful within one round.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/datayurei/robyou/enrollment"
)

// saveInterval keeps disk writes down while a job is polling: a merge that
// happens sooner than this after the last write only marks the round dirty.
const saveInterval = 5 * time.Second

// Course types, mirroring config.Type* and enrollment.CourseType*.
const (
	TypeInPlan = "inplan"
	TypePublic = "public"
)

// safeXkid guards the cache filename, which is derived from server data.
var safeXkid = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Entry is one cached teaching class.
type Entry struct {
	LessonID     string `json:"lesson_id"`
	EnrollID     string `json:"enroll_id"`
	Type         string `json:"type"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	GroupName    string `json:"group_name,omitempty"`
	Credit       string `json:"credit"`
	Teacher      string `json:"teacher"`
	Time         string `json:"time"`
	Location     string `json:"location"`
	Campus       string `json:"campus"`
	Enrolled     string `json:"enrolled"`
	Remaining    string `json:"remaining"`
	TeachMode    string `json:"teach_mode,omitempty"`
	ConflictNote string `json:"conflict_note,omitempty"`
	// Keywords records which searches surfaced this course. For in-plan
	// courses it is the only clue about how to find them again.
	Keywords    []string  `json:"keywords,omitempty"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// Round is the cache for one enrollment round.
type Round struct {
	Xkid            string     `json:"xkid"`
	UpdatedAt       time.Time  `json:"updated_at"`
	PublicFetchedAt *time.Time `json:"public_fetched_at,omitempty"`
	Entries         []Entry    `json:"entries"`
}

// Stats summarises a round's cache for the GUI.
type Stats struct {
	Xkid            string     `json:"xkid"`
	Total           int        `json:"total"`
	InPlan          int        `json:"inplan"`
	Public          int        `json:"public"`
	UpdatedAt       time.Time  `json:"updated_at"`
	PublicFetchedAt *time.Time `json:"public_fetched_at,omitempty"`
	// Keywords are the in-plan searches that have contributed so far.
	Keywords []string `json:"keywords"`
}

// MergeResult reports what one merge changed.
type MergeResult struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Total   int `json:"total"`
}

// Query is a local search over the cache. It never touches the network.
type Query struct {
	Text          string `json:"text"`
	Type          string `json:"type"`
	OnlyAvailable bool   `json:"only_available"`
	Sort          string `json:"sort"`
	Limit         int    `json:"limit"`
}

// Results is the answer to a local search.
type Results struct {
	Entries []Entry `json:"entries"`
	// Matched is the number of entries the query matched, before Limit.
	Matched int `json:"matched"`
	Total   int `json:"total"`
}

type roundCache struct {
	round    Round
	entries  map[string]int // key -> index into round.Entries
	dirty    bool
	lastSave time.Time
}

// Store holds per-round caches and persists them under a directory.
type Store struct {
	mu     sync.Mutex
	dir    string
	rounds map[string]*roundCache
}

// NewStore returns a store writing one JSON file per round under dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir, rounds: make(map[string]*roundCache)}
}

// Merge folds a page of search results into the round's cache and reports what
// changed. Seat counts and other mutable fields are refreshed on every sighting.
func (s *Store) Merge(xkid, courseType, keyword string, courses []enrollment.Course) MergeResult {
	if !validXkid(xkid) || len(courses) == 0 {
		return MergeResult{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cache := s.cacheLocked(xkid)
	now := time.Now()
	result := MergeResult{}
	keyword = strings.TrimSpace(keyword)

	for _, course := range courses {
		if strings.TrimSpace(course.LessonID) == "" {
			continue
		}

		key := entryKey(courseType, course.LessonID)
		index, exists := cache.entries[key]
		if !exists {
			entry := entryFromCourse(course, courseType, now)
			addKeyword(&entry, keyword)
			cache.round.Entries = append(cache.round.Entries, entry)
			cache.entries[key] = len(cache.round.Entries) - 1
			result.Added++
			continue
		}

		entry := &cache.round.Entries[index]
		firstSeen := entry.FirstSeenAt
		keywords := entry.Keywords
		*entry = entryFromCourse(course, courseType, now)
		entry.FirstSeenAt = firstSeen
		entry.Keywords = keywords
		addKeyword(entry, keyword)
		result.Updated++
	}

	if result.Added > 0 || result.Updated > 0 {
		cache.round.UpdatedAt = now
		cache.dirty = true
		s.maybeSaveLocked(cache)
	}
	result.Total = len(cache.round.Entries)

	return result
}

// MarkPublicFetched records that the whole public catalog was just fetched.
func (s *Store) MarkPublicFetched(xkid string) {
	if !validXkid(xkid) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cache := s.cacheLocked(xkid)
	now := time.Now()
	cache.round.PublicFetchedAt = &now
	cache.dirty = true
	s.saveLocked(cache)
}

// Search runs a local query over the cache.
func (s *Store) Search(xkid string, query Query) Results {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !validXkid(xkid) {
		return Results{Entries: []Entry{}}
	}

	cache := s.cacheLocked(xkid)
	text := strings.ToLower(strings.TrimSpace(query.Text))
	courseType := strings.TrimSpace(query.Type)

	matched := make([]Entry, 0, len(cache.round.Entries))
	for _, entry := range cache.round.Entries {
		if courseType != "" && entry.Type != courseType {
			continue
		}
		if query.OnlyAvailable && parseCount(entry.Remaining) <= 0 {
			continue
		}
		if text != "" && !entryMatches(entry, text) {
			continue
		}
		matched = append(matched, entry)
	}

	sortEntries(matched, query.Sort)

	results := Results{Matched: len(matched), Total: len(cache.round.Entries)}
	if query.Limit > 0 && len(matched) > query.Limit {
		matched = matched[:query.Limit]
	}
	results.Entries = matched

	return results
}

// Stats summarises one round's cache.
func (s *Store) Stats(xkid string) Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !validXkid(xkid) {
		return Stats{Keywords: []string{}}
	}

	cache := s.cacheLocked(xkid)
	stats := Stats{
		Xkid:            xkid,
		Total:           len(cache.round.Entries),
		UpdatedAt:       cache.round.UpdatedAt,
		PublicFetchedAt: cache.round.PublicFetchedAt,
	}

	seen := map[string]bool{}
	keywords := []string{}
	for _, entry := range cache.round.Entries {
		switch entry.Type {
		case TypePublic:
			stats.Public++
		default:
			stats.InPlan++
			for _, keyword := range entry.Keywords {
				if keyword != "" && !seen[keyword] {
					seen[keyword] = true
					keywords = append(keywords, keyword)
				}
			}
		}
	}
	sort.Strings(keywords)
	stats.Keywords = keywords

	return stats
}

// Clear drops a round's cache from memory and disk.
func (s *Store) Clear(xkid string) error {
	if !validXkid(xkid) {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.rounds, xkid)
	if err := os.Remove(s.path(xkid)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove cache %s: %w", s.path(xkid), err)
	}

	return nil
}

// Flush writes every dirty round to disk.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for _, cache := range s.rounds {
		if !cache.dirty {
			continue
		}
		if err := s.saveLocked(cache); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// LatestXkid returns the most recently updated cached round, so the GUI can
// show a catalog before the current round is known.
func (s *Store) LatestXkid() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var newest string
	var newestAt time.Time

	for xkid, cache := range s.rounds {
		if cache.round.UpdatedAt.After(newestAt) {
			newest, newestAt = xkid, cache.round.UpdatedAt
		}
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return newest, newest != ""
	}
	for _, item := range entries {
		name := item.Name()
		if item.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestAt) {
			newest, newestAt = strings.TrimSuffix(name, ".json"), info.ModTime()
		}
	}

	return newest, newest != ""
}

// cacheLocked returns the round's cache, loading it from disk on first use.
func (s *Store) cacheLocked(xkid string) *roundCache {
	if cache, ok := s.rounds[xkid]; ok {
		return cache
	}

	cache := &roundCache{
		round:   Round{Xkid: xkid, Entries: []Entry{}},
		entries: map[string]int{},
	}

	if data, err := os.ReadFile(s.path(xkid)); err == nil {
		var round Round
		if err := json.Unmarshal(data, &round); err == nil {
			round.Xkid = xkid
			cache.round = round
			for i, entry := range round.Entries {
				cache.entries[entryKey(entry.Type, entry.LessonID)] = i
			}
		}
	}

	s.rounds[xkid] = cache
	return cache
}

func (s *Store) maybeSaveLocked(cache *roundCache) {
	if time.Since(cache.lastSave) < saveInterval {
		return
	}
	s.saveLocked(cache)
}

func (s *Store) saveLocked(cache *roundCache) error {
	if s.dir == "" {
		cache.dirty = false
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}

	data, err := json.MarshalIndent(cache.round, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(s.path(cache.round.Xkid), data, 0o600); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}

	cache.dirty = false
	cache.lastSave = time.Now()

	return nil
}

func (s *Store) path(xkid string) string {
	return filepath.Join(s.dir, xkid+".json")
}

func entryFromCourse(course enrollment.Course, courseType string, now time.Time) Entry {
	return Entry{
		LessonID:     course.LessonID,
		EnrollID:     course.EnrollID,
		Type:         courseType,
		Code:         course.Code,
		Name:         course.Name,
		GroupName:    course.GroupName,
		Credit:       course.Credit,
		Teacher:      course.Teacher,
		Time:         enrollment.CleanHTMLBreaks(course.Time),
		Location:     course.Location,
		Campus:       course.Campus,
		Enrolled:     course.Enrolled,
		Remaining:    course.Remaining,
		TeachMode:    course.TeachMode,
		ConflictNote: course.ConflictNote,
		FirstSeenAt:  now,
		LastSeenAt:   now,
	}
}

func addKeyword(entry *Entry, keyword string) {
	if keyword == "" {
		return
	}
	for _, existing := range entry.Keywords {
		if existing == keyword {
			return
		}
	}
	entry.Keywords = append(entry.Keywords, keyword)
}

func entryMatches(entry Entry, text string) bool {
	fields := []string{
		entry.Name, entry.Code, entry.Teacher, entry.Location,
		entry.Campus, entry.Time, entry.TeachMode, entry.GroupName,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), text) {
			return true
		}
	}
	return false
}

func sortEntries(entries []Entry, mode string) {
	switch mode {
	case "remaining":
		sort.SliceStable(entries, func(i, j int) bool {
			return parseCount(entries[i].Remaining) > parseCount(entries[j].Remaining)
		})
	case "teacher":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Teacher < entries[j].Teacher })
	case "code":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Code < entries[j].Code })
	case "recent":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].LastSeenAt.After(entries[j].LastSeenAt) })
	default:
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	}
}

func parseCount(value string) int {
	count, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return count
}

func entryKey(courseType, lessonID string) string {
	return courseType + ":" + lessonID
}

func validXkid(xkid string) bool {
	return safeXkid.MatchString(xkid)
}
