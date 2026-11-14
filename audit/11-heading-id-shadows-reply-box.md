# 11. A heading can take the reply box's element id

**Status: Fixed** in `9f93157`. The slugger reserves `dp-main`, `dp-reply-h` and `ftree`. Test: `internal/render/a11y_test.go`, `TestHeadingCannotTakeTheReplyBoxID` and `TestHeadingsAvoidChromeIDs`, outside the `audit` tag.

**Severity: Low.** The document cannot forge the reply form or read its
token. It can make the form's accessible name its own text: a screen reader
announces the document's heading as the label of the first-party answer box.

## Affected code

- `internal/render/markdown.go:541-543` (`newSlugger`): heading ids are kept
  unique among headings and against `dp-main` only.
- `internal/render/markdown.go:640-641`: the baked reply box is
  `<section class="dp-reply" aria-labelledby="dp-reply-h">` with
  `<h2 id="dp-reply-h">`.

## Impact

`## dp reply h` slugs to `dp-reply-h` and comes earlier in the page than the
reply box, so `aria-labelledby` resolves to it. Non-ASCII characters are
dropped by the slug, so the visible heading text can be anything followed by
those three words. The series tree's `ftree` id can be duplicated the same
way; nothing refers to it, so that one is only invalid HTML.

## Failing test

`internal/render/audit_inline_test.go` (build tag `audit`).

```go
func TestAuditHeadingCannotTakeTheReplyBoxID(t *testing.T) {
	page := string(Markdown([]byte("# Lesson\n\n## dp reply h\n\ntext\n"), Options{ReplySlug: "lesson", ReplyToken: "tok"}))
	if n := strings.Count(page, `id="dp-reply-h"`); n != 1 {
		t.Errorf(`id="dp-reply-h" appears %d times, want 1 (the reply box's own heading)`, n)
	}
}
```

```
$ go test -tags audit -run TestAuditHeadingCannotTake -count=1 ./internal/render/
--- FAIL: TestAuditHeadingCannotTakeTheReplyBoxID (0.00s)
    audit_inline_test.go:49: id="dp-reply-h" appears 2 times, want 1 (the reply box's own heading)
FAIL
```

## Smallest fix

Seed `newSlugger`'s `used` set with every id the chrome writes
(`dp-reply-h`, `ftree`, and any other fixed id in `markdown.go`,
`flayout.go` and `colophon.go`), as it is seeded with `dp-main` today. A
colliding heading then gets `dp-reply-h-2`.
