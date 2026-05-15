package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKeywordEvaluator_SubstringMatchesCaseSensitive(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "Forbidden", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: true},
	})
	eval, err := newKeywordEvaluator(rules)
	require.NoError(t, err)

	block, flags := eval.Evaluate("This is a Forbidden phrase.")
	require.NotNil(t, block)
	require.Equal(t, "Forbidden", block.Matched)
	require.Empty(t, flags)

	// Mismatched case must not match when case_insensitive=false
	block, _ = eval.Evaluate("this is a forbidden phrase")
	require.Nil(t, block)
}

func TestKeywordEvaluator_SubstringCaseInsensitive(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "HELLO", MatchType: KeywordMatchSubstring, CaseInsensitive: true, Action: KeywordActionBlock, Enabled: true},
	})
	eval, err := newKeywordEvaluator(rules)
	require.NoError(t, err)

	block, _ := eval.Evaluate("say hello to me")
	require.NotNil(t, block)
	require.Equal(t, "HELLO", block.Matched)
}

func TestKeywordEvaluator_RegexMatchesAndReportsActualSubstring(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: `[Ss]ecret\d+`, MatchType: KeywordMatchRegex, Action: KeywordActionBlock, Enabled: true},
	})
	eval, err := newKeywordEvaluator(rules)
	require.NoError(t, err)

	block, _ := eval.Evaluate("please leak Secret42 now")
	require.NotNil(t, block)
	require.Equal(t, "Secret42", block.Matched)
}

func TestKeywordEvaluator_RegexCaseInsensitiveInjectsFlag(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: `SECRET\d+`, MatchType: KeywordMatchRegex, CaseInsensitive: true, Action: KeywordActionBlock, Enabled: true},
	})
	eval, err := newKeywordEvaluator(rules)
	require.NoError(t, err)

	block, _ := eval.Evaluate("any secret42 here")
	require.NotNil(t, block)
	require.Equal(t, "secret42", block.Matched)
}

func TestKeywordEvaluator_BlockShortCircuitsFurtherBlocksButFlagsContinue(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "alpha", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: true},
		{Pattern: "beta", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: true},
		{Pattern: "gamma", MatchType: KeywordMatchSubstring, Action: KeywordActionFlag, Enabled: true},
		{Pattern: "delta", MatchType: KeywordMatchSubstring, Action: KeywordActionFlag, Enabled: true},
	})
	eval, err := newKeywordEvaluator(rules)
	require.NoError(t, err)

	block, flags := eval.Evaluate("alpha beta gamma delta")
	require.NotNil(t, block)
	require.Equal(t, "alpha", block.Matched, "first block rule should win")
	require.Len(t, flags, 2)
	require.ElementsMatch(t, []string{"gamma", "delta"}, []string{flags[0].Matched, flags[1].Matched})
}

func TestKeywordEvaluator_DisabledRulesSkipped(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "alpha", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: false},
		{Pattern: "beta", MatchType: KeywordMatchSubstring, Action: KeywordActionFlag, Enabled: false},
	})
	eval, err := newKeywordEvaluator(rules)
	require.NoError(t, err)

	block, flags := eval.Evaluate("alpha beta gamma")
	require.Nil(t, block)
	require.Empty(t, flags)
}

func TestKeywordEvaluator_EmptyTextReturnsNothing(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "alpha", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: true},
	})
	eval, err := newKeywordEvaluator(rules)
	require.NoError(t, err)

	block, flags := eval.Evaluate("")
	require.Nil(t, block)
	require.Empty(t, flags)
}

func TestKeywordEvaluator_ChineseSubstringMatch(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "测试拦截", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: true},
	})
	eval, err := newKeywordEvaluator(rules)
	require.NoError(t, err)

	block, _ := eval.Evaluate("这是一个 测试拦截 的样例")
	require.NotNil(t, block)
	require.Equal(t, "测试拦截", block.Matched)
}

func TestNormalizeKeywordRules_TrimsAndDropsEmptyPatterns(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "  abc  ", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock},
		{Pattern: "   ", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock},
		{Pattern: "", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock},
	})
	require.Len(t, rules, 1)
	require.Equal(t, "abc", rules[0].Pattern)
	require.NotEmpty(t, rules[0].ID, "ID should be auto-filled")
}

func TestNormalizeKeywordRules_DefaultsAndDedups(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "abc"},                               // empty match_type & action → substring+block
		{Pattern: "abc"},                               // duplicate of first
		{Pattern: "abc", MatchType: "FuZzY"},           // unknown match_type → falls back to substring → duplicate of first
		{Pattern: "abc", MatchType: KeywordMatchRegex}, // different match_type → kept
	})
	require.Len(t, rules, 2)
	require.Equal(t, KeywordMatchSubstring, rules[0].MatchType)
	require.Equal(t, KeywordActionBlock, rules[0].Action)
	require.Equal(t, KeywordMatchRegex, rules[1].MatchType)
}

func TestValidateKeywordRules_RejectsInvalidRegex(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "[unterminated", MatchType: KeywordMatchRegex, Action: KeywordActionBlock, Enabled: true},
	})
	err := validateKeywordRules(rules)
	require.Error(t, err)
}

func TestValidateKeywordRules_AcceptsValidRegex(t *testing.T) {
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: `\d+`, MatchType: KeywordMatchRegex, Action: KeywordActionBlock, Enabled: true},
	})
	require.NoError(t, validateKeywordRules(rules))
}

func TestKeywordRulesHash_StableAcrossOrderingAndChangesOnEdit(t *testing.T) {
	a := []KeywordRule{
		{ID: "id-1", Pattern: "alpha", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: true},
		{ID: "id-2", Pattern: "beta", MatchType: KeywordMatchSubstring, Action: KeywordActionFlag, Enabled: true},
	}
	b := []KeywordRule{a[1], a[0]} // same set, different slice order — hash sorts by ID before serializing
	require.Equal(t, keywordRulesHash(a), keywordRulesHash(b))

	c := make([]KeywordRule, len(a))
	copy(c, a)
	c[0].Pattern = "alpha-changed"
	require.NotEqual(t, keywordRulesHash(a), keywordRulesHash(c))

	require.Equal(t, "empty", keywordRulesHash(nil))
}

func TestServiceGetKeywordEvaluator_CachesUntilRulesChange(t *testing.T) {
	svc := &ContentModerationService{}
	rules := normalizeKeywordRules([]KeywordRule{
		{Pattern: "alpha", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: true},
	})
	first := svc.getKeywordEvaluator(rules)
	second := svc.getKeywordEvaluator(rules)
	require.NotNil(t, first)
	require.Same(t, first, second, "identical rules should return the cached evaluator")

	modified := normalizeKeywordRules([]KeywordRule{
		{Pattern: "alpha", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: true},
		{Pattern: "beta", MatchType: KeywordMatchSubstring, Action: KeywordActionFlag, Enabled: true},
	})
	third := svc.getKeywordEvaluator(modified)
	require.NotSame(t, first, third, "changed rule set should invalidate the cache")
}

func TestServiceTestKeywords_ReturnsBlockAndFlagsAndRejectsInvalidRegex(t *testing.T) {
	svc := &ContentModerationService{}

	result, err := svc.TestKeywords(context.Background(), TestKeywordsInput{
		Text: "the quick brown fox",
		Rules: []KeywordRule{
			{Pattern: "quick", MatchType: KeywordMatchSubstring, Action: KeywordActionBlock, Enabled: true},
			{Pattern: "fox", MatchType: KeywordMatchSubstring, Action: KeywordActionFlag, Enabled: true},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Block)
	require.Equal(t, "quick", result.Block.Matched)
	require.Len(t, result.Flags, 1)
	require.Equal(t, "fox", result.Flags[0].Matched)

	_, err = svc.TestKeywords(context.Background(), TestKeywordsInput{
		Text:  "anything",
		Rules: []KeywordRule{{Pattern: "[bad", MatchType: KeywordMatchRegex, Action: KeywordActionBlock, Enabled: true}},
	})
	require.Error(t, err)
}
