# 10. Emphasis markers inside a link destination rewrite the `href` after it was checked

**Severity: Low.** No script can be injected: the inserted tags contain no
quote, so they stay inside the quoted attribute, and nothing is removed from
the destination. The effect is a link that no longer goes where the allowlist
check saw it go, and, in one form, emphasis that is never closed and runs to
the end of the page, over the baked reply box and the colophon.

## Affected code

- `internal/render/markdown.go:1224-1227` (`inline`): `reLink` turns the
  link into `<a href="…">` first; `reBold`, `reItalic` and `reItalicU` then
  run over the whole string, the generated tag included. `safeURL` judged the
  destination before those passes.

## Impact

`[a](/x*y) *z` renders as `<a href="/x<em>y" …>a</a> </em>z`: the link goes
to `/x%3Cem%3Ey`. `*a [b](/c*d) e` renders as `<em>a <a href="/c</em>d" …>b</a>
e`: the closing tag is inside the attribute, so the `<em>` opened before the
link stays open, and the browser carries it into every following block. The
renderer's promise is that source text cannot change the markup around it;
here it changes an attribute the renderer wrote and the styling of content
the renderer adds itself. The same residue reaches the page title and table
of contents as visible garbage (`d" rel="noopener noreferrer">b`), still
escaped.

## Failing test

`internal/render/audit_inline_test.go` (build tag `audit`).

```go
func TestAuditEmphasisDoesNotRewriteLinkDestinations(t *testing.T) {
	for _, src := range []string{
		"[a](/x*y) *z",
		"*a [b](/c*d) e",
		"**a [b](/c**d) e",
		"[a](/_b) c_ d",
	} {
		got := render(src)
		for _, m := range auditHref.FindAllStringSubmatch(got, -1) {
			if strings.ContainsAny(m[1], "<>") {
				t.Errorf("render(%q): markup inside a link destination: href=%q", src, m[1])
			}
		}
	}
}
```

```
$ go test -tags audit -run TestAuditEmphasis -count=1 ./internal/render/
--- FAIL: TestAuditEmphasisDoesNotRewriteLinkDestinations (0.00s)
    audit_inline_test.go:35: render("[a](/x*y) *z"): markup inside a link destination: href="/x<em>y"
    audit_inline_test.go:35: render("*a [b](/c*d) e"): markup inside a link destination: href="/c</em>d"
    audit_inline_test.go:35: render("**a [b](/c**d) e"): markup inside a link destination: href="/c</strong>d"
    audit_inline_test.go:35: render("[a](/_b) c_ d"): markup inside a link destination: href="/<em>b"
FAIL
```

## Smallest fix

Treat links the way code spans are already treated: lift each rendered
`<a …>` opening tag into a NUL-framed placeholder before the emphasis passes
and restore it afterwards (`liftCodeSpans` / `restoreSpans` are the model),
so the passes never see the destination.
