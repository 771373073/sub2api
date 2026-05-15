package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

	"github.com/google/uuid"
)

const (
	KeywordMatchSubstring = "substring"
	KeywordMatchRegex     = "regex"
	KeywordActionBlock    = "block"
	KeywordActionFlag     = "flag"

	maxKeywordPatternRunes = 1000
	maxKeywordNoteRunes    = 200
	maxKeywordRules        = 5000
)

// KeywordRule defines a single local moderation rule applied before the OpenAI moderation call.
type KeywordRule struct {
	ID              string `json:"id"`
	Pattern         string `json:"pattern"`
	MatchType       string `json:"match_type"`
	CaseInsensitive bool   `json:"case_insensitive"`
	Action          string `json:"action"`
	Note            string `json:"note"`
	Enabled         bool   `json:"enabled"`
}

// KeywordMatchResult represents a single rule match against the input text.
type KeywordMatchResult struct {
	Rule    KeywordRule `json:"rule"`
	Matched string      `json:"matched"`
}

// keywordEvaluator caches compiled regex patterns for a set of rules.
type keywordEvaluator struct {
	rules    []KeywordRule
	compiled map[string]*regexp.Regexp
}

func newKeywordEvaluator(rules []KeywordRule) (*keywordEvaluator, error) {
	compiled := make(map[string]*regexp.Regexp, len(rules))
	for _, rule := range rules {
		if !rule.Enabled || rule.MatchType != KeywordMatchRegex {
			continue
		}
		expr := rule.Pattern
		if rule.CaseInsensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("compile keyword regex %q: %w", rule.Pattern, err)
		}
		compiled[rule.ID] = re
	}
	return &keywordEvaluator{rules: rules, compiled: compiled}, nil
}

// Evaluate scans text against all enabled rules. Returns the first block match (if any)
// and every flag match. After a block is found, remaining block rules are skipped but
// flag scanning continues so admins still see all observation matches.
func (e *keywordEvaluator) Evaluate(text string) (*KeywordMatchResult, []KeywordMatchResult) {
	if e == nil || text == "" {
		return nil, nil
	}
	var block *KeywordMatchResult
	var flags []KeywordMatchResult
	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}
		if block != nil && rule.Action == KeywordActionBlock {
			continue
		}
		matched, ok := e.matchOne(text, rule)
		if !ok {
			continue
		}
		hit := KeywordMatchResult{Rule: rule, Matched: matched}
		switch rule.Action {
		case KeywordActionBlock:
			if block == nil {
				blockCopy := hit
				block = &blockCopy
			}
		case KeywordActionFlag:
			flags = append(flags, hit)
		}
	}
	return block, flags
}

func (e *keywordEvaluator) matchOne(text string, rule KeywordRule) (string, bool) {
	switch rule.MatchType {
	case KeywordMatchRegex:
		re := e.compiled[rule.ID]
		if re == nil {
			return "", false
		}
		loc := re.FindStringIndex(text)
		if loc == nil {
			return "", false
		}
		return text[loc[0]:loc[1]], true
	case KeywordMatchSubstring, "":
		needle := rule.Pattern
		if rule.CaseInsensitive {
			// strings.ToLower can change byte length for some Unicode points (e.g. ı/İ),
			// so do not slice back into the original text — return the rule pattern verbatim.
			if !strings.Contains(strings.ToLower(text), strings.ToLower(needle)) {
				return "", false
			}
			return rule.Pattern, true
		}
		idx := strings.Index(text, needle)
		if idx < 0 {
			return "", false
		}
		return text[idx : idx+len(needle)], true
	default:
		return "", false
	}
}

// normalizeKeywordRules trims, dedups by content key (pattern+match_type+case_insensitive),
// and assigns missing IDs. It does not validate regex compilability — that is
// validateKeywordRules' job. The total-count limit is enforced by validateKeywordRules
// (returns an error) rather than silently truncating here.
func normalizeKeywordRules(rules []KeywordRule) []KeywordRule {
	if len(rules) == 0 {
		return []KeywordRule{}
	}
	seenID := make(map[string]struct{}, len(rules))
	seenContent := make(map[string]struct{}, len(rules))
	out := make([]KeywordRule, 0, len(rules))
	for _, rule := range rules {
		rule.Pattern = strings.TrimSpace(rule.Pattern)
		if rule.Pattern == "" {
			continue
		}
		rule.MatchType = normalizeKeywordMatchType(rule.MatchType)
		rule.Action = normalizeKeywordAction(rule.Action)
		rule.Note = trimRunes(strings.TrimSpace(rule.Note), maxKeywordNoteRunes)
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			rule.ID = uuid.NewString()
		}
		if _, ok := seenID[rule.ID]; ok {
			rule.ID = uuid.NewString()
		}
		contentKey := keywordContentKey(rule)
		if _, ok := seenContent[contentKey]; ok {
			continue
		}
		seenID[rule.ID] = struct{}{}
		seenContent[contentKey] = struct{}{}
		out = append(out, rule)
	}
	return out
}

// validateKeywordRules is called on the already-normalized slice — it only catches what
// normalize cannot silently fix (regex syntax errors, oversized patterns, total count).
// Exceeding maxKeywordRules is an error here (not a silent truncation in normalize) so
// callers receive an explicit rejection rather than losing rules silently.
func validateKeywordRules(rules []KeywordRule) error {
	if len(rules) > maxKeywordRules {
		return infraerrors.BadRequest("TOO_MANY_CONTENT_MODERATION_KEYWORD_RULES", fmt.Sprintf("关键词规则数量不能超过 %d 条", maxKeywordRules))
	}
	for _, rule := range rules {
		pattern := strings.TrimSpace(rule.Pattern)
		if pattern == "" {
			return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_KEYWORD_PATTERN", "关键词内容不能为空")
		}
		if len([]rune(pattern)) > maxKeywordPatternRunes {
			return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_KEYWORD_PATTERN", fmt.Sprintf("关键词长度不能超过 %d 字符", maxKeywordPatternRunes))
		}
		if rule.MatchType == KeywordMatchRegex {
			expr := pattern
			if rule.CaseInsensitive {
				expr = "(?i)" + expr
			}
			if _, err := regexp.Compile(expr); err != nil {
				return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_KEYWORD_REGEX", fmt.Sprintf("关键词正则编译失败 %q: %v", pattern, err))
			}
		}
	}
	return nil
}

func normalizeKeywordMatchType(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), KeywordMatchRegex) {
		return KeywordMatchRegex
	}
	return KeywordMatchSubstring
}

func normalizeKeywordAction(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), KeywordActionFlag) {
		return KeywordActionFlag
	}
	return KeywordActionBlock
}

func keywordContentKey(rule KeywordRule) string {
	return strings.Join([]string{rule.Pattern, "|", rule.MatchType, "|", strconv.FormatBool(rule.CaseInsensitive)}, "")
}

// keywordRulesHash returns a stable digest of a rule set. Used as a cache key so the
// evaluator only recompiles regex patterns when evaluation-relevant fields change.
// Note is intentionally excluded: it is a display-only field that does not affect
// matching behaviour, so editing a note must not bust the evaluator cache.
func keywordRulesHash(rules []KeywordRule) string {
	if len(rules) == 0 {
		return "empty"
	}
	type evalFields struct {
		ID              string `json:"id"`
		Pattern         string `json:"pattern"`
		MatchType       string `json:"match_type"`
		CaseInsensitive bool   `json:"case_insensitive"`
		Action          string `json:"action"`
		Enabled         bool   `json:"enabled"`
	}
	sorted := make([]evalFields, len(rules))
	for i, r := range rules {
		sorted[i] = evalFields{
			ID:              r.ID,
			Pattern:         r.Pattern,
			MatchType:       r.MatchType,
			CaseInsensitive: r.CaseInsensitive,
			Action:          r.Action,
			Enabled:         r.Enabled,
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	raw, err := json.Marshal(sorted)
	if err != nil {
		return "err"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
