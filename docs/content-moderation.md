# Content Moderation

## Architecture

Sub2API includes a multi-stage content moderation system to detect and block harmful user input before it reaches downstream AI models. Moderation decisions flow through the following stages:

```
Incoming Request
      ↓
[Hash Pre-check] (if enabled)
      ↓
[Keyword Evaluator] (local substring/regex rules)
      ↓
[Sample Rate Gate] (stochastic sampling)
      ↓
[OpenAI Moderation API] (omni-moderation-latest)
      ↓
[Decision: Allow / Block / Flag]
      ↓
[Side Effects] (ban accumulation, email alerts, logging)
```

The keyword evaluator runs **sample-rate independent**, meaning all requests are checked against keyword rules regardless of sampling decisions. Hash pre-checks and OpenAI calls respect the sample rate.

## Configuration Storage

All moderation configuration is stored in the `settings` table under the key `content_moderation_config` as a single JSON object. No database schema changes were required. Configuration changes are backward compatible: missing fields fall back to safe defaults.

| Field | Type | Purpose |
|-------|------|---------|
| `enabled` | bool | Master on/off switch |
| `mode` | string | `off` \| `observe` \| `pre_block` |
| `base_url` | string | OpenAI API endpoint (e.g., `https://api.openai.com`) |
| `model` | string | Moderation model name (e.g., `omni-moderation-latest`) |
| `api_key` / `api_keys` | string / array | API credentials for OpenAI |
| `timeout_ms` | int | HTTP timeout for moderation calls (default: 3000ms) |
| `sample_rate` | int | Percentage of requests sent to OpenAI (0–100) |
| `all_groups` | bool | Apply to all user groups |
| `group_ids` | array | Specific group IDs to apply moderation |
| `record_non_hits` | bool | Log requests that pass moderation |
| `thresholds` | object | Per-category score thresholds (0.0–1.0) |
| `keywords` | array | Keyword rules with patterns, match types, and actions |
| `worker_count` | int | Async worker threads (default: 4, max: 32) |
| `queue_size` | int | Async task queue depth (default: 32768) |
| `block_status` | int | HTTP status when blocked (default: 403) |
| `block_message` | string | User-facing message on block |
| `email_on_hit` | bool | Send email alerts when flagged/blocked |
| `auto_ban_enabled` | bool | Enable auto-ban on violation threshold |
| `ban_threshold` | int | Violations to trigger auto-ban (default: 10) |
| `violation_window_hours` | int | Time window for violation counting (default: 720h = 30d) |
| `retry_count` | int | Retry attempts for transient API errors (default: 2) |
| `hit_retention_days` | int | How long to keep flagged/blocked logs (default: 180) |
| `non_hit_retention_days` | int | How long to keep passing logs (default: 3) |
| `pre_hash_check_enabled` | bool | Enable content hash deduplication cache |

## Modes

Moderation operates in three modes:

### `off`

Moderation is disabled. No OpenAI calls or keyword checks occur.

### `observe`

Requests are checked and violations are logged, but **requests are never blocked**. The response is sent to the user regardless of the moderation result. Use this mode to gather baseline data before enabling enforcement.

| Stage | Behavior |
|-------|----------|
| **Hash Pre-check** | If cached hash is blocked, log but allow request |
| **Keyword Flag hits** | Logged immediately (always evaluated, regardless of block) |
| **Keyword Block hit** | Logged, OpenAI call short-circuited, request allowed (no API cost) |
| **OpenAI Block** | If no keyword block hit: enqueue async worker to call OpenAI, allow request immediately |
| **Side Effects** | Email alerts sent, OpenAI violations accumulate toward ban_threshold (keyword hits do **not**) |

### `pre_block`

Violations are logged and requests are blocked immediately. Use in production to actively block harmful content.

| Stage | Behavior |
|-------|----------|
| **Hash Pre-check** | If cached hash is blocked, reject request (403) |
| **Keyword Flag hits** | Logged immediately (always evaluated, regardless of block) |
| **Keyword Block hit** | Reject request immediately (403), log match, OpenAI call short-circuited |
| **OpenAI Block** | If no keyword block hit: call OpenAI synchronously; reject if any category exceeds threshold |
| **Side Effects** | Email alerts sent, OpenAI violations accumulate toward ban_threshold (keyword hits do **not**) |

**Important:** Keyword block hits do **not** count toward `ban_threshold` accumulation. Only OpenAI violations and hash block hits increment the ban counter. This prevents accidental mass-bans from misconfigured keyword rules.

## OpenAI Threshold Tuning

Sub2API uses OpenAI's `omni-moderation-latest` model, which scores 13 content categories on a scale of 0.0 to 1.0. A request is flagged if **any** category score exceeds its configured threshold.

### Category Thresholds

| Category | Default Threshold | Notes |
|----------|-------------------|-------|
| `harassment` | 0.98 | General harassment (very high bar) |
| `harassment/threatening` | 0.90 | Threats of violence or harm |
| `hate` | 0.65 | Slurs, dehumanization, hate speech |
| `hate/threatening` | 0.65 | Hate speech + threats |
| `illicit` | 0.95 | Illegal activities or substances |
| `illicit/violent` | 0.95 | Illegal violent acts |
| `self-harm` | 0.65 | Self-injury or suicide ideation |
| `self-harm/intent` | 0.85 | Expressed intent to self-harm |
| `self-harm/instructions` | 0.65 | Instructions or guides for self-harm |
| `sexual` | 0.65 | Sexual content (non-minor) |
| `sexual/minors` | 0.65 | Child sexual abuse material (CSAM) |
| `violence` | 0.95 | Graphic violence (high bar) |
| `violence/graphic` | 0.95 | Extremely graphic violence |

### Threshold Adjustment Guidelines

**Raising a threshold (e.g., 0.65 → 0.75):**
- Fewer false positives; catches only high-confidence violations
- Use when legitimate content is being blocked
- Risk: more harmful content passes

**Lowering a threshold (e.g., 0.90 → 0.75):**
- Catches more potential violations with less confidence
- Use when harmful content is slipping through
- Risk: more false positives and operational noise

For sensitive categories like `sexual/minors` and `self-harm`, keep thresholds low (0.65) even if it means higher false positive rates—the cost of a false negative is unacceptable.

## Keyword Blocklist

The keyword evaluator provides a fast, deterministic layer for catching known-bad content before calling OpenAI. Keyword rules are stored in the `keywords` array of the moderation config.

### KeywordRule Structure

```json
{
  "id": "rule-uuid",
  "pattern": "badword",
  "match_type": "substring",
  "case_insensitive": true,
  "action": "block",
  "note": "Flagged for review",
  "enabled": true
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | UUID, auto-assigned if missing |
| `pattern` | string | Substring or regex pattern (max 1000 runes) |
| `match_type` | string | `substring` or `regex` |
| `case_insensitive` | bool | Case-insensitive matching (default: false) |
| `action` | string | `block` or `flag` |
| `note` | string | Human-readable reason (max 200 runes, advisory) |
| `enabled` | bool | Include rule in evaluation |

### Match Types

**substring:** Case-sensitive or case-insensitive string containment via `strings.Contains` or `strings.ToLower`. No regex interpretation. For Unicode case-insensitivity, the matched text returned is the rule pattern itself, not a slice of the original input (avoids byte-length changes in non-ASCII text).

**regex:** Case-insensitive regexes are prefixed with `(?i)` at compile time. Invalid regex expressions are rejected during rule validation; partial match anywhere in the text is sufficient (no anchors required). Matched text is the substring found in the input.

### Actions

**block:** If this rule matches, the request is blocked immediately in `pre_block` mode (returns 403). In `observe` mode, the match is logged but the request is allowed. Block hits do **not** accumulate toward `ban_threshold`.

**flag:** Match is recorded in logs for observation and analysis, but never blocks the request. Useful for detecting emerging threats without causing user-facing friction.

### Evaluation Rules

1. All enabled rules are evaluated against the input text.
2. The evaluator returns the first `block` match (if any) and all `flag` matches.
3. Once a `block` match is found, remaining `block` rules are skipped for performance, but `flag` rules continue to be evaluated so admins see the full picture.
4. Rules are evaluated sample-rate independent—every request is checked, regardless of OpenAI sampling.
5. Keyword-only requests (no OpenAI call) do not require API keys.

### Limitations

- Maximum 5000 rules per configuration.
- Keyword rules must be part of an enabled moderation config (`cfg.Enabled == true` and `cfg.Mode != "off"`). There is no keyword-only mode.
- Substring matching is case-sensitive by default; use `case_insensitive: true` for looser matching.
- Regex patterns are compiled at config update time; invalid patterns are rejected immediately.

## API Endpoints

All moderation admin endpoints require authentication and `risk_control` permission.

### GET /api/admin/risk-control/config

Retrieve the current moderation configuration.

**Request:**
```bash
curl -X GET https://your-api.example.com/api/admin/risk-control/config \
  -H "Authorization: Bearer YOUR_TOKEN"
```

**Response (200 OK):**
```json
{
  "enabled": true,
  "mode": "pre_block",
  "base_url": "https://api.openai.com",
  "model": "omni-moderation-latest",
  "timeout_ms": 3000,
  "sample_rate": 100,
  "all_groups": true,
  "thresholds": {
    "harassment": 0.98,
    "hate": 0.65,
    "sexual": 0.65,
    "violence": 0.95
  },
  "keywords": [
    {
      "id": "rule-1",
      "pattern": "badword123",
      "match_type": "substring",
      "case_insensitive": true,
      "action": "block",
      "enabled": true
    }
  ],
  "ban_threshold": 10,
  "auto_ban_enabled": true
}
```

### PUT /api/admin/risk-control/config

Update the moderation configuration. All fields are optional; omitted fields retain their current values.

**Request:**
```bash
curl -X PUT https://your-api.example.com/api/admin/risk-control/config \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "mode": "pre_block",
    "sample_rate": 100,
    "thresholds": {
      "harassment": 0.98,
      "hate": 0.60,
      "sexual": 0.65,
      "violence": 0.95
    },
    "keywords": [
      {
        "pattern": "new-bad-term",
        "match_type": "substring",
        "case_insensitive": true,
        "action": "block",
        "enabled": true,
        "note": "Added 2026-05-15"
      }
    ]
  }'
```

**Response (200 OK):**
```json
{
  "enabled": true,
  "mode": "pre_block",
  "thresholds": { ... },
  "keywords": [ ... ]
}
```

**Error Responses:**
- `400 Bad Request` – Invalid JSON, invalid regex, threshold out of range (0.0–1.0), or >5000 keyword rules.
- `401 Unauthorized` – Missing or invalid authentication token.
- `403 Forbidden` – Insufficient permissions.

### POST /api/admin/risk-control/keywords/test

Dry-run keyword evaluation against a sample text without modifying config.

**Request:**
```bash
curl -X POST https://your-api.example.com/api/admin/risk-control/keywords/test \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "keywords": [
      {
        "pattern": "test.*pattern",
        "match_type": "regex",
        "case_insensitive": false,
        "action": "block",
        "enabled": true
      }
    ],
    "text": "This is a test-pattern in the input."
  }'
```

**Response (200 OK):**
```json
{
  "block": {
    "rule": {
      "id": "test-rule-1",
      "pattern": "test.*pattern",
      "match_type": "regex",
      "case_insensitive": false,
      "action": "block",
      "enabled": true
    },
    "matched": "test-pattern"
  },
  "flags": []
}
```

- `block` is the first blocking rule match (or `null` if none).
- `flags` is an array of all flag rule matches.

## Testing Recommendations

### Local Validation

1. **Test keyword rules in isolation:**
   ```bash
   # Test a regex rule before saving
   curl -X POST http://localhost:8080/api/admin/risk-control/keywords/test \
     -d '{
       "keywords": [{"pattern": "^\\d{3}-\\d{2}-\\d{4}$", "match_type": "regex", "action": "block"}],
       "text": "My SSN is 123-45-6789"
     }' | jq
   ```

2. **Test threshold changes in `observe` mode:**
   - Set `mode: "observe"` and `sample_rate: 100`.
   - Lower thresholds incrementally (e.g., `hate: 0.65 → 0.60 → 0.55`).
   - Monitor logs for false positive rates.
   - Once stable, switch to `pre_block`.

3. **Verify side effects:**
   - Trigger a violation (use a test keyword rule).
   - Confirm logs are written to `content_moderation_logs`.
   - If `email_on_hit: true`, verify alert emails are sent.
   - If `auto_ban_enabled: true`, monitor that violation counts increment correctly.

4. **Test hash pre-check (if enabled):**
   - Submit identical content twice.
   - First request goes to OpenAI; second is resolved from hash cache.
   - Verify log entries have matching `input_hash`.

### Integration Testing

- Submit requests with known-bad content (e.g., hateful language, explicit material).
- Verify correct mode behavior (observe vs. pre_block).
- Test edge cases: very long inputs (>12000 runes), mixed scripts (CJK + Latin), special characters.
- Monitor async worker queue depth under load.

## Caveats and Known Limitations

### Keyword and OpenAI Scope

Both keyword rules and OpenAI thresholds share the same group scope (`all_groups` / `group_ids`). If moderation is enabled for group A only, keyword evaluation also applies only to group A. There is no way to apply different keyword rules to different groups.

### No Keyword-Only Mode

Keyword rules require `cfg.Enabled == true` and `cfg.Mode != "off"`. Standalone keyword filtering without OpenAI is not supported. To use keyword rules, moderation must be enabled globally.

### Case-Insensitive Substring Matching

For case-insensitive substring rules, the `Matched` field in logs contains the rule pattern verbatim, not the original matched substring. This is necessary because `strings.ToLower` can change byte length for some Unicode characters (e.g., Turkish ı/İ). If exact matched text is critical, use regex rules with explicit lowercase patterns or accept that case-insensitive substrings report the pattern.

### Ban Threshold Excludes Keyword Hits

Keyword block hits do not count toward `ban_threshold`. Only OpenAI violations (category score > threshold) and hash pre-check blocks count. This is intentional: keyword rules are deterministic and more likely to be misconfigured; auto-ban should only trigger on high-confidence OpenAI detections.

### Sample Rate Does Not Apply to Keywords

Keyword evaluation runs on every request regardless of `sample_rate`. This ensures that explicitly defined local rules are always enforced. The sample rate only gates OpenAI API calls.

### No Real-Time Config Reload Across Instances

Configuration changes are stored in the database and read on each request, so changes should propagate within seconds. However, in-memory caches (compiled regex patterns, keyword evaluators) may not update synchronously across all server instances. Restart instances if immediate consistency is required.
