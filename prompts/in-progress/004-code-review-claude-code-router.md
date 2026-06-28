---
status: committing
summary: Full code review completed, 2 Critical fix prompts generated for factory error wrapping and alias-preinit loop
execution_id: claude-code-router-exec-004-code-review-claude-code-router
dark-factory-version: v0.187.11
created: "2026-06-28T16:01:31Z"
queued: "2026-06-28T16:11:49Z"
started: "2026-06-28T16:11:51Z"
completed: "2026-06-28T16:15:18Z"
---

<summary>
- Service reviewed using full automated code review with all specialist agents
- Fix prompts generated for each Critical or Important finding
- Each fix prompt is independently verifiable and scoped to one concern
- No code changes made — review-only prompt that produces fix prompts
- Clean services produce no fix prompts
</summary>

<objective>
Run a full code review of `.` (the claude-code-router repo root, single service) and generate a fix prompt for each Critical or Important finding.
</objective>

<context>
Read `docs/dod.md` for Definition of Done criteria.

Read the 3 most-recent-numbered completed prompts under `prompts/completed/` to understand prompt style and XML tag structure.

Service directory: `.` (root)
Fix prompts output directory: `prompts/`
</context>

<requirements>

## 1. Run Code Review

Run `/coding:code-review full .` to get a comprehensive review with all specialist agents.

Collect the consolidated findings categorized as:
- **Must Fix (Critical)** — will generate fix prompts
- **Should Fix (Important)** — will generate fix prompts
- **Nice to Have** — skip, do NOT generate prompts

## 2. Generate Fix Prompts

For each Critical or Important finding (or group of related findings in the same file/package), write a prompt file to `prompts/`.

**Filename:** `review-claude-code-router-<fix-description>.md`

Each fix prompt must follow this exact structure:

```
---
status: draft
created: "<current UTC timestamp in ISO8601>"
---

<summary>
5-10 plain-language bullets. No file paths, struct names, or function signatures.
</summary>

<objective>
What to fix and why (1-3 sentences). End state, not steps.
</objective>

<context>
Read `docs/dod.md` for Definition of Done criteria.

Files to read before making changes (read ALL first):
- list specific files, anchored by function/type name
</context>

<requirements>
Numbered, specific, unambiguous steps.
Anchor by function/type name (line numbers as hints only — they go stale).
Include function signatures where helpful.
</requirements>

<constraints>
- Only change files in `.` (this repo)
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Wrap errors with context using `fmt.Errorf("...: %w", err)` — never bare `return err`
</constraints>

<verification>
make precommit
</verification>
```

**Grouping rules:**
- One concern per prompt (e.g., "fix error wrapping in package X")
- Group coupled findings that must change together
- Split unrelated findings into separate prompts
- If order matters, prefix filenames with `1-`, `2-`, `3-`

## 3. Summary

Print a summary of findings and generated prompt files.

</requirements>

<constraints>
- Do NOT modify any source code — this is a review-only prompt
- Only write files to the prompts inbox directory
- Never write to `in-progress/` or `completed/` subdirectories
- Never number prompt filenames — dark-factory assigns numbers on approve
- Repo-relative paths only in generated prompts (no absolute, no `~/`)
- If no findings at Critical/Important level → report clean bill of health, generate no prompts
</constraints>

<verification>
This prompt only generates markdown files — no code changes, no build needed.
ls prompts/review-claude-code-router-*.md 2>/dev/null || echo "no findings — clean bill of health"
</verification>
